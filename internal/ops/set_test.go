package ops_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/apply"
	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/ops"
)

const site = "events { worker_connections 16; }\n" +
	"http {\n" +
	"  server {\n" +
	"    listen 8080;\n" +
	"    server_name a.test;\n" +
	"    add_header X-A \"b; c\";\n" +
	"  }\n" +
	"}\n"

func parse(t *testing.T, src string) (*config.Tree, string) {
	t.Helper()

	dir := t.TempDir()
	root := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(root, []byte(src), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)
	return tree, root
}

func refOf(t *testing.T, tree *config.Tree, directive string) string {
	t.Helper()

	var found *config.Node
	tree.Walk(func(n *config.Node) bool {
		if found == nil && n.Directive == directive {
			found = n
		}
		return true
	})
	require.NotNilf(t, found, "no %s in the fixture", directive)
	return found.Ref
}

func TestSetReplacesOnlyTheArguments(t *testing.T) {
	tree, root := parse(t, site)

	p, err := ops.Set(tree, root, refOf(t, tree, "listen"), []string{"8443", "ssl"})
	require.NoError(t, err)
	require.NoError(t, p.Validate())
	require.NoError(t, p.Verify(tree, root))
	require.Len(t, p.Edits, 1)

	// The edit covers the head and nothing else: the terminator is not in it.
	require.Equal(t, "listen 8080", p.Edits[0].Before)
	require.Equal(t, "listen 8443 ssl", p.Edits[0].After)

	res, err := apply.Run(apply.Options{
		Plan: p, Tree: tree, Root: root, Validate: func() error { return nil }})
	require.NoError(t, err)
	require.Len(t, res.Written, 1)

	got, err := os.ReadFile(root)
	require.NoError(t, err)
	// Everything else byte for byte, indentation included.
	require.Equal(t, "events { worker_connections 16; }\n"+
		"http {\n"+
		"  server {\n"+
		"    listen 8443 ssl;\n"+
		"    server_name a.test;\n"+
		"    add_header X-A \"b; c\";\n"+
		"  }\n"+
		"}\n", string(got))
}

// An argument holding a semicolon has to come out quoted, and the operation has
// to CONFIRM that rather than assume it. The fixture already contains one, so
// the round trip is over a value nginx really accepts.
func TestSetQuotesAnArgumentThatNeedsIt(t *testing.T) {
	tree, root := parse(t, site)

	p, err := ops.Set(tree, root, refOf(t, tree, "add_header"),
		[]string{"X-B", "value; with { braces } and #hash"})
	require.NoError(t, err)

	_, err = apply.Run(apply.Options{
		Plan: p, Tree: tree, Root: root, Validate: func() error { return nil }})
	require.NoError(t, err)

	// The proof is a re-read: the value that comes back is the value asked for,
	// whatever quoting was used to get there.
	again, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)

	var header *config.Node
	again.Walk(func(n *config.Node) bool {
		if header == nil && n.Directive == "add_header" {
			header = n
		}
		return true
	})
	require.NotNil(t, header)
	require.Equal(t, []string{"X-B", "value; with { braces } and #hash"}, header.Args)
}

func TestSetRefuses(t *testing.T) {
	t.Run("a ref that names nothing", func(t *testing.T) {
		tree, root := parse(t, site)
		_, err := ops.Set(tree, root, root+"#nope", []string{"1"})
		requireRefusal(t, err, ops.CodeRefNotFound)
	})

	t.Run("no arguments at all", func(t *testing.T) {
		tree, root := parse(t, site)
		_, err := ops.Set(tree, root, refOf(t, tree, "listen"), nil)
		requireRefusal(t, err, ops.CodeInvalidArguments)
	})

	t.Run("a change that changes nothing", func(t *testing.T) {
		tree, root := parse(t, site)
		_, err := ops.Set(tree, root, refOf(t, tree, "listen"), []string{"8080"})
		requireRefusal(t, err, ops.CodeNoChange)
	})

	// "if" rewrites its own arguments inside crossplane, so ArgSpans is nil and
	// rebuilding the head from Args would change what the directive says.
	t.Run("an if directive", func(t *testing.T) {
		tree, root := parse(t, "events { worker_connections 16; }\n"+
			"http { server { location / {\n"+
			"if ($request_method = POST) { return 405; }\n"+
			"} } }\n")
		_, err := ops.Set(tree, root, refOf(t, tree, "if"), []string{"$a", "=", "b"})
		requireRefusal(t, err, ops.CodeUnsupportedTarget)
	})

	// A comment between the arguments is recorded in HeadComments so a rewrite
	// does not erase it. Refusing beats guessing where it belongs.
	t.Run("a directive with a comment between its arguments", func(t *testing.T) {
		tree, root := parse(t, "events { worker_connections 16; }\n"+
			"http { server {\nserver_name a.test # prod\n  b.test;\n} }\n")

		var target *config.Node
		tree.Walk(func(n *config.Node) bool {
			if target == nil && n.Directive == "server_name" {
				target = n
			}
			return true
		})
		require.NotNil(t, target)
		require.NotEmpty(t, target.HeadComments, "the premise: the comment is inside the head")

		_, err := ops.Set(tree, root, target.Ref, []string{"c.test"})
		requireRefusal(t, err, ops.CodeUnsupportedTarget)
	})
}

// The self-check is what makes the quoting safe, so it has to be shown working:
// an argument that cannot be expressed at all has to be refused rather than
// written badly.
//
// A newline inside an argument is the case. nginx has no escape for it in a
// quoted string -- a quoted argument may span lines, but then the newline is
// part of the value -- so the text produced does parse, and this asserts what
// actually happens rather than a guess about it.
func TestSetChecksItsOwnOutput(t *testing.T) {
	tree, root := parse(t, site)

	p, err := ops.Set(tree, root, refOf(t, tree, "server_name"), []string{"a\nb"})
	if err != nil {
		requireRefusal(t, err, ops.CodeInvalidArguments)
		return
	}

	// If it was accepted, then the value has to survive a real round trip --
	// which is the property the self-check is there to guarantee.
	_, applyErr := apply.Run(apply.Options{
		Plan: p, Tree: tree, Root: root, Validate: func() error { return nil }})
	require.NoError(t, applyErr)

	again, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)

	var name *config.Node
	again.Walk(func(n *config.Node) bool {
		if name == nil && n.Directive == "server_name" {
			name = n
		}
		return true
	})
	require.NotNil(t, name)
	require.Equal(t, []string{"a\nb"}, name.Args,
		"the operation accepted an argument it could not represent")
}

func TestRefusalCodesAreLiterals(t *testing.T) {
	require.Equal(t, "ops_ref_not_found", string(ops.CodeRefNotFound))
	require.Equal(t, "ops_unsupported_target", string(ops.CodeUnsupportedTarget))
	require.Equal(t, "ops_invalid_arguments", string(ops.CodeInvalidArguments))
	require.Equal(t, "ops_no_change", string(ops.CodeNoChange))
}

func requireRefusal(t *testing.T, err error, want ops.RefusalCode) {
	t.Helper()

	require.Error(t, err, "expected a refusal with code %q", want)
	got, ok := ops.CodeOf(err)
	require.Truef(t, ok, "not an ops refusal: %v", err)
	require.Equalf(t, want, got, "wrong code for: %v", err)
}
