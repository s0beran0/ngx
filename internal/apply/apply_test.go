package apply_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/apply"
	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/plan"
)

const simple = "events { worker_connections 16; }\n" +
	"http {\n  server {\n    listen 8080;\n    server_name a.test;\n  }\n}\n"

type world struct {
	dir  string
	root string
	tree *config.Tree
	plan plan.Plan
}

// setup writes a configuration, parses it, and builds a plan that changes the
// listen directive's head -- the smallest real edit.
func setup(t *testing.T) *world {
	t.Helper()

	dir := t.TempDir()
	root := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(root, []byte(simple), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)

	var target *config.Node
	tree.Walk(func(n *config.Node) bool {
		if target == nil && n.Directive == "listen" {
			target = n
		}
		return true
	})
	require.NotNil(t, target)

	return &world{
		dir:  dir,
		root: root,
		tree: tree,
		plan: plan.Plan{
			Root:       root,
			ConfigHash: tree.Hash,
			Edits: []plan.Edit{{
				File:   target.File,
				Ref:    target.Ref,
				Span:   target.HeadSpan,
				Before: string(tree.Files[0].Source[target.HeadSpan.Start:target.HeadSpan.End]),
				After:  "listen 8443 ssl",
				Reason: "set listen",
			}},
		},
	}
}

func ok() error { return nil }

func TestApplyWritesExactlyTheEditedBytes(t *testing.T) {
	w := setup(t)

	res, err := apply.Run(apply.Options{Plan: &w.plan, Tree: w.tree, Root: w.root, Validate: ok})
	require.NoError(t, err)
	require.Equal(t, []string{w.root}, res.Written)
	require.Empty(t, res.RolledBack)
	require.Empty(t, res.NotRestored)

	got, err := os.ReadFile(w.root)
	require.NoError(t, err)

	// The whole point of a span substitution: the file differs from the
	// original in exactly one place, and everything else is byte for byte what
	// it was -- indentation, blank lines, line endings included.
	want := strings.Replace(simple, "listen 8080", "listen 8443 ssl", 1)
	require.Equal(t, want, string(got))
}

// A stale plan costs nothing. That is what the order of the steps buys, and it
// is asserted by checking the file is untouched rather than by trusting the
// error.
func TestAStalePlanWritesNothing(t *testing.T) {
	w := setup(t)
	w.plan.ConfigHash = strings.Repeat("0", 64)

	res, err := apply.Run(apply.Options{Plan: &w.plan, Tree: w.tree, Root: w.root, Validate: ok})
	require.Error(t, err)

	code, isFailure := apply.CodeOf(err)
	require.True(t, isFailure)
	require.Equal(t, apply.CodeVerifyFailed, code)
	require.Empty(t, res.Written)

	got, err := os.ReadFile(w.root)
	require.NoError(t, err)
	require.Equal(t, simple, string(got), "the file was touched despite the refusal")
}

// The rollback the plan asks for: nginx refuses the result AFTER a successful
// write, and the file comes back byte-identical.
func TestARefusedConfigurationIsPutBackByteForByte(t *testing.T) {
	w := setup(t)
	refused := errors.New("nginx: [emerg] a port already in use")

	res, err := apply.Run(apply.Options{
		Plan: &w.plan, Tree: w.tree, Root: w.root,
		Validate: func() error { return refused },
	})

	require.Error(t, err)
	code, _ := apply.CodeOf(err)
	require.Equal(t, apply.CodeValidateFailed, code)
	require.ErrorIs(t, err, refused, "the reason nginx gave has to survive to the caller")

	require.Equal(t, []string{w.root}, res.RolledBack)
	require.Empty(t, res.NotRestored)

	got, err := os.ReadFile(w.root)
	require.NoError(t, err)
	require.Equal(t, simple, string(got))
}

// Validate has to run AFTER the bytes are on disk, or it validates the old
// configuration and says nothing about the new one.
func TestValidateSeesTheNewContentAndNotTheOld(t *testing.T) {
	w := setup(t)

	var seen string
	_, err := apply.Run(apply.Options{
		Plan: &w.plan, Tree: w.tree, Root: w.root,
		Validate: func() error {
			b, readErr := os.ReadFile(w.root)
			require.NoError(t, readErr)
			seen = string(b)
			return nil
		},
	})
	require.NoError(t, err)
	require.Contains(t, seen, "listen 8443 ssl",
		"validate ran against the old bytes, so it can say nothing about the change")
}

// Two files, one of which cannot be written. Everything that landed before it
// is put back, and the file that failed is not written to at all -- restoring a
// file this apply never changed would be the one write it has no reason to make.
func TestAFailedWriteRollsBackWhatLandedAndLeavesTheRestAlone(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "nginx.conf")
	sub := filepath.Join(dir, "site.conf")

	require.NoError(t, os.WriteFile(root, []byte(
		"events { worker_connections 16; }\nhttp { include site.conf; }\n"), 0o644))
	require.NoError(t, os.WriteFile(sub, []byte("server { listen 8080; }\n"), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)

	edits := make([]plan.Edit, 0, 2)
	for _, f := range tree.Files {
		var target *config.Node
		walk(f.Nodes, func(n *config.Node) {
			if target == nil && (n.Directive == "worker_connections" || n.Directive == "listen") {
				target = n
			}
		})
		require.NotNilf(t, target, "no target in %s", f.Path)
		edits = append(edits, plan.Edit{
			File:   target.File,
			Ref:    target.Ref,
			Span:   target.HeadSpan,
			Before: string(f.Source[target.HeadSpan.Start:target.HeadSpan.End]),
			After:  strings.ToUpper(string(f.Source[target.HeadSpan.Start:target.HeadSpan.End])),
		})
	}
	require.Len(t, edits, 2)

	// Make the SECOND file unwritable by removing write permission from its
	// directory... which would break the first too. Instead, delete it: Stat
	// fails, so writeAtomically refuses before creating anything.
	//
	// Which file is second is decided by Plan.Files(), which sorts.
	p := plan.Plan{Root: root, ConfigHash: tree.Hash, Edits: edits}
	files := p.Files()
	require.Len(t, files, 2)
	doomed := files[1]
	survivor := files[0]
	originalSurvivor := readFile(t, survivor)
	require.NoError(t, os.Remove(doomed))

	res, err := apply.Run(apply.Options{Plan: &p, Tree: tree, Root: root, Validate: ok})
	require.Error(t, err)
	code, _ := apply.CodeOf(err)
	require.Equal(t, apply.CodeWriteFailed, code)

	require.Equal(t, []string{survivor}, res.RolledBack)
	require.Empty(t, res.NotRestored)
	require.Empty(t, res.Written, "a failed apply reports nothing as written")

	require.Equal(t, originalSurvivor, readFile(t, survivor),
		"the file written before the failure was not put back")
	require.NoFileExists(t, doomed, "the file that could not be written was created anyway")
}

// The mode is part of what the file was. A 0640 configuration that comes back
// 0644 is a defect even when every byte is right.
//
// The mode here is 0640 for a reason found by negative verification: with 0600
// this test passed even with the chmod removed, because os.CreateTemp creates
// at 0600 and the temporary file happened to already be right. The assertion
// was true by coincidence, which is the same failure as a check that cannot
// fail -- it just needed a different constant to reveal itself.
func TestTheModeSurvivesTheWrite(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o644, 0o600} {
		t.Run(mode.String(), func(t *testing.T) {
			w := setup(t)
			require.NoError(t, os.Chmod(w.root, mode))

			_, err := apply.Run(apply.Options{Plan: &w.plan, Tree: w.tree, Root: w.root, Validate: ok})
			require.NoError(t, err)

			info, err := os.Stat(w.root)
			require.NoError(t, err)
			require.Equal(t, mode, info.Mode().Perm(),
				"the mode changed: either the umask won over the file's own permissions, "+
					"or the temporary file's mode was left in place")
		})
	}
}

// A rename leaves no temporary file behind, and neither does a failure. A
// directory full of .ngx-apply-* files is how an operator discovers this
// package the hard way.
func TestNoTemporaryFileIsLeftBehind(t *testing.T) {
	t.Run("after success", func(t *testing.T) {
		w := setup(t)
		_, err := apply.Run(apply.Options{Plan: &w.plan, Tree: w.tree, Root: w.root, Validate: ok})
		require.NoError(t, err)
		requireNoTemps(t, w.dir)
	})

	t.Run("after a rollback", func(t *testing.T) {
		w := setup(t)
		_, err := apply.Run(apply.Options{
			Plan: &w.plan, Tree: w.tree, Root: w.root,
			Validate: func() error { return errors.New("refused") },
		})
		require.Error(t, err)
		requireNoTemps(t, w.dir)
	})
}

// The state nobody wants, reported rather than hidden: a write landed, Validate
// refused it, and the restore also failed.
//
// It is provoked by making the directory read-only after the write, so the
// temporary file the rollback needs cannot be created. The assertion is that
// the error says so, with the file named -- a summary line saying "rolled back"
// would be a lie the operator acts on.
func TestARollbackThatFailsIsReportedAndNotSwallowed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory does not stop root from writing")
	}

	w := setup(t)
	var failed bool

	_, err := apply.Run(apply.Options{
		Plan: &w.plan, Tree: w.tree, Root: w.root,
		Validate: func() error {
			// The write has landed by now. Take away the directory's write
			// permission so the rollback's temporary file cannot be created.
			require.NoError(t, os.Chmod(w.dir, 0o500))
			failed = true
			return errors.New("nginx refused it")
		},
	})
	require.True(t, failed, "the validator never ran, so this test proved nothing")
	t.Cleanup(func() { _ = os.Chmod(w.dir, 0o700) })

	require.Error(t, err)
	code, isFailure := apply.CodeOf(err)
	require.True(t, isFailure)
	require.Equal(t, apply.CodeRollbackFailed, code,
		"a failed rollback has to have its own code: an operator who reads "+
			"validate_failed believes the disk is back to normal")

	var f *apply.Failure
	require.True(t, errors.As(err, &f))
	require.Equal(t, []string{w.root}, f.Result.NotRestored,
		"the files that are in an unknown state have to be named, not counted")

	// And the disk really is in the new state, which is what makes the report
	// necessary rather than pessimistic.
	require.Contains(t, readFile(t, w.root), "listen 8443 ssl")
}

// Edits are applied from the highest offset down, and this is the test that
// fails if that ever changes: two edits in one file, both of which change
// length, so applying them the other way round would corrupt the second.
func TestTwoEditsInOneFileBothLand(t *testing.T) {
	w := setup(t)

	var listen, serverName *config.Node
	w.tree.Walk(func(n *config.Node) bool {
		switch n.Directive {
		case "listen":
			if listen == nil {
				listen = n
			}
		case "server_name":
			if serverName == nil {
				serverName = n
			}
		}
		return true
	})
	require.NotNil(t, listen)
	require.NotNil(t, serverName)

	src := w.tree.Files[0].Source
	w.plan.Edits = []plan.Edit{
		{
			File: listen.File, Ref: listen.Ref, Span: listen.HeadSpan,
			Before: string(src[listen.HeadSpan.Start:listen.HeadSpan.End]),
			After:  "listen 8443 ssl http2",
		},
		{
			File: serverName.File, Ref: serverName.Ref, Span: serverName.HeadSpan,
			Before: string(src[serverName.HeadSpan.Start:serverName.HeadSpan.End]),
			After:  "server_name b",
		},
	}

	_, err := apply.Run(apply.Options{Plan: &w.plan, Tree: w.tree, Root: w.root, Validate: ok})
	require.NoError(t, err)

	want := strings.Replace(simple, "listen 8080", "listen 8443 ssl http2", 1)
	want = strings.Replace(want, "server_name a.test", "server_name b", 1)
	require.Equal(t, want, readFile(t, w.root))
}

// An empty After is a removal, and it has to leave the file parseable rather
// than producing a special case somewhere.
func TestAnEmptyReplacementRemovesTheBytes(t *testing.T) {
	w := setup(t)

	var target *config.Node
	w.tree.Walk(func(n *config.Node) bool {
		if target == nil && n.Directive == "server_name" {
			target = n
		}
		return true
	})
	require.NotNil(t, target)

	// The whole directive, terminator included, so what is left is valid.
	w.plan.Edits = []plan.Edit{{
		File: target.File, Ref: target.Ref, Span: target.Span,
		Before: string(w.tree.Files[0].Source[target.Span.Start:target.Span.End]),
		After:  "",
	}}

	_, err := apply.Run(apply.Options{Plan: &w.plan, Tree: w.tree, Root: w.root, Validate: ok})
	require.NoError(t, err)

	got := readFile(t, w.root)
	require.NotContains(t, got, "server_name")
	// It still parses: a removal that leaves a broken file is a removal that
	// nginx will refuse, and the point of a span is that it does not.
	_, err = config.Parse(config.ParseOptions{Path: w.root})
	require.NoError(t, err, "what was left does not parse:\n%s", got)
}

// The failure codes are a contract: they reach the JSON envelope and a caller
// branches on them.
func TestFailureCodesAreLiterals(t *testing.T) {
	require.Equal(t, "apply_verify_failed", string(apply.CodeVerifyFailed))
	require.Equal(t, "apply_write_failed", string(apply.CodeWriteFailed))
	require.Equal(t, "apply_validate_failed", string(apply.CodeValidateFailed))
	require.Equal(t, "apply_rollback_failed", string(apply.CodeRollbackFailed))
}

// --- helpers ---------------------------------------------------------------

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func requireNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.Falsef(t, strings.HasPrefix(e.Name(), ".ngx-apply-"),
			"a temporary file was left behind: %s", e.Name())
	}
}

func walk(nodes []*config.Node, fn func(*config.Node)) {
	for _, n := range nodes {
		fn(n)
		walk(n.Block, fn)
	}
}

// The Result is never nil, whatever happened. This is asserted because the
// first version of Run returned nil on the refusal path, and the first test
// that read Result on that path panicked -- which is what every caller would
// eventually do.
func TestTheResultIsNeverNil(t *testing.T) {
	w := setup(t)

	t.Run("on success", func(t *testing.T) {
		res, err := apply.Run(apply.Options{Plan: &w.plan, Tree: w.tree, Root: w.root, Validate: ok})
		require.NoError(t, err)
		require.NotNil(t, res)
	})

	t.Run("on a refusal before any write", func(t *testing.T) {
		stale := w.plan
		stale.ConfigHash = strings.Repeat("0", 64)
		res, err := apply.Run(apply.Options{Plan: &stale, Tree: w.tree, Root: w.root, Validate: ok})
		require.Error(t, err)
		require.NotNil(t, res)
	})

	t.Run("on missing options", func(t *testing.T) {
		res, err := apply.Run(apply.Options{})
		require.Error(t, err)
		require.NotNil(t, res)
	})
}

// recordingElevator records whether privilege was used at all, which is the
// property that matters: escalating when it was not needed means writing
// somebody's configuration as root for no reason.
type recordingElevator struct {
	writes  []string
	removes []string
}

func (r *recordingElevator) WriteFile(_ context.Context, path string, data []byte, mode os.FileMode, uid, gid int) error {
	r.writes = append(r.writes, path)
	return os.WriteFile(path, data, mode)
}

func (r *recordingElevator) Remove(_ context.Context, path string) error {
	r.removes = append(r.removes, path)
	return os.Remove(path)
}

// A file the user can write is written WITHOUT privilege, even when privilege
// is available. This is the assertion that keeps "we have sudo" from becoming
// "we use sudo".
func TestPrivilegeIsNotUsedWhenItIsNotNeeded(t *testing.T) {
	w := setup(t)
	elevator := &recordingElevator{}

	_, err := apply.Run(apply.Options{
		Plan: &w.plan, Tree: w.tree, Root: w.root,
		Validate:   ok,
		Privileged: elevator,
	})
	require.NoError(t, err)
	require.Empty(t, elevator.writes,
		"privilege was used on a file the invoking user can write perfectly well")
}

// And when the write really is refused, privilege is used and the apply
// succeeds. Provoked by a read-only directory, which is what a root-owned
// /etc/nginx/conf.d looks like to an ordinary user.
func TestPrivilegeIsUsedWhenTheWriteIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory does not stop root from writing")
	}

	w := setup(t)

	// The elevator here can still write because the TEST is not really
	// unprivileged -- it restores the mode first. What is being checked is the
	// DECISION to escalate, not sudo itself, which the transport tests cover
	// against a real one.
	require.NoError(t, os.Chmod(w.dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(w.dir, 0o700) })

	restoring := &restoringElevator{dir: w.dir}
	_, err := apply.Run(apply.Options{
		Plan: &w.plan, Tree: w.tree, Root: w.root,
		Validate:   ok,
		Privileged: restoring,
	})
	require.NoError(t, err, "the write was refused and privilege did not rescue it")
	require.Equal(t, []string{w.root}, restoring.writes)
}

// restoringElevator stands in for sudo: it makes the directory writable, does
// the write, and puts the mode back -- which is what elevating actually
// achieves, without needing a real sudo in a unit test.
type restoringElevator struct {
	dir    string
	writes []string
}

func (r *restoringElevator) WriteFile(_ context.Context, path string, data []byte, mode os.FileMode, uid, gid int) error {
	if err := os.Chmod(r.dir, 0o700); err != nil {
		return err
	}
	defer os.Chmod(r.dir, 0o500)
	r.writes = append(r.writes, path)
	return os.WriteFile(path, data, mode)
}

func (r *restoringElevator) Remove(_ context.Context, path string) error {
	if err := os.Chmod(r.dir, 0o700); err != nil {
		return err
	}
	defer os.Chmod(r.dir, 0o500)
	return os.Remove(path)
}
