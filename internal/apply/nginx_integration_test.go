//go:build integration

package apply_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/apply"
	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/plan"
)

// The oracle the plan demands: every write validated by a real nginx rather
// than by a parser.
//
// A parser agreeing with itself proves the edit is well formed. Only the binary
// says whether the result is a configuration nginx would load, and the rollback
// path is only meaningful if the thing that refuses it is the thing that
// refuses in production.
const luaBenchContainer = "ngx-bench-lua"

func requireNginx(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available; bring the bench up to run this")
	}
	if err := exec.Command("docker", "inspect", luaBenchContainer).Run(); err != nil {
		t.Skip("the bench is not up: run `make bench-lua-up`")
	}
}

// nginxValidate copies the configuration into the container and runs a real
// `nginx -t` over it.
func nginxValidate(t *testing.T, dir string) apply.Validate {
	t.Helper()
	return func() error {
		cp := exec.Command("docker", "cp", dir+"/.", luaBenchContainer+":/tmp/applyconf")
		if out, err := cp.CombinedOutput(); err != nil {
			t.Fatalf("could not copy the configuration into the container: %v\n%s", err, out)
		}
		out, err := exec.Command("docker", "exec", luaBenchContainer,
			"openresty", "-t", "-c", "/tmp/applyconf/nginx.conf").CombinedOutput()
		if err != nil {
			return &nginxRefusal{output: string(out)}
		}
		return nil
	}
}

type nginxRefusal struct{ output string }

func (e *nginxRefusal) Error() string { return strings.TrimSpace(e.output) }

const valid = "events { worker_connections 16; }\n" +
	"http {\n  server {\n    listen 8080;\n    server_name a.test;\n  }\n}\n"

func setupOnDisk(t *testing.T, src string) (string, string, *config.Tree) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755))
	root := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(root, []byte(src), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)
	return dir, root, tree
}

func editOf(t *testing.T, tree *config.Tree, directive, after string) plan.Edit {
	t.Helper()

	var target *config.Node
	tree.Walk(func(n *config.Node) bool {
		if target == nil && n.Directive == directive {
			target = n
		}
		return true
	})
	require.NotNilf(t, target, "no %s in the fixture", directive)

	return plan.Edit{
		File:   target.File,
		Ref:    target.Ref,
		Span:   target.HeadSpan,
		Before: string(tree.Files[0].Source[target.HeadSpan.Start:target.HeadSpan.End]),
		After:  after,
	}
}

// A valid change: real nginx accepts the result and the file stays.
func TestARealNginxAcceptsAValidEdit(t *testing.T) {
	requireNginx(t)

	dir, root, tree := setupOnDisk(t, valid)
	p := plan.Plan{Root: root, ConfigHash: tree.Hash,
		Edits: []plan.Edit{editOf(t, tree, "listen", "listen 8443")}}

	res, err := apply.Run(apply.Options{
		Plan: &p, Tree: tree, Root: root, Validate: nginxValidate(t, dir)})
	require.NoError(t, err)
	require.Equal(t, []string{root}, res.Written)

	got, err := os.ReadFile(root)
	require.NoError(t, err)
	require.Contains(t, string(got), "listen 8443")
}

// The rollback, with the real binary doing the refusing.
//
// The edit is syntactically fine and semantically impossible: a listen
// directive with an argument nginx does not accept. A parser would let it
// through, which is the whole reason this test uses nginx.
func TestARealNginxRefusalRollsTheFileBack(t *testing.T) {
	requireNginx(t)

	dir, root, tree := setupOnDisk(t, valid)
	p := plan.Plan{Root: root, ConfigHash: tree.Hash,
		Edits: []plan.Edit{editOf(t, tree, "listen", "listen 8080 nonsense_parameter")}}

	res, err := apply.Run(apply.Options{
		Plan: &p, Tree: tree, Root: root, Validate: nginxValidate(t, dir)})

	require.Error(t, err, "nginx accepted an invalid parameter, so this test proves nothing")
	code, _ := apply.CodeOf(err)
	require.Equal(t, apply.CodeValidateFailed, code)
	require.Equal(t, []string{root}, res.RolledBack)
	require.Empty(t, res.NotRestored)

	got, err := os.ReadFile(root)
	require.NoError(t, err)
	require.Equal(t, valid, string(got),
		"the file was not put back byte for byte after a real nginx refused it")
}
