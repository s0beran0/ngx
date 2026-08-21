package ops_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/apply"
	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/ops"
)

// The oracle the plan names for add and rm: adding a directive and removing it
// again returns the file BYTE-IDENTICAL. It is stronger than any expectation
// written by hand, because it does not depend on my idea of what the right
// indentation was.
func TestAddThenRemoveReturnsTheFileByteForByte(t *testing.T) {
	for name, src := range map[string]string{
		"two-space indent": "events { worker_connections 16; }\n" +
			"http {\n  server {\n    listen 8080;\n  }\n}\n",
		"tab indent": "events { worker_connections 16; }\n" +
			"http {\n\tserver {\n\t\tlisten 8080;\n\t}\n}\n",
		"no indent": "events { worker_connections 16; }\n" +
			"http {\nserver {\nlisten 8080;\n}\n}\n",
		"crlf": "events { worker_connections 16; }\r\n" +
			"http {\r\n  server {\r\n    listen 8080;\r\n  }\r\n}\r\n",
		"blank lines around": "events { worker_connections 16; }\n" +
			"http {\n\n  server {\n\n    listen 8080;\n\n  }\n\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			tree, root := parse(t, src)

			p, err := ops.Add(tree, root, refOf(t, tree, "server"), "server_name", []string{"a.test"})
			require.NoError(t, err)
			_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
				Validate: func() error { return nil }})
			require.NoError(t, err)

			added, err := os.ReadFile(root)
			require.NoError(t, err)
			require.Contains(t, string(added), "server_name a.test;")

			// Re-read: the plan is anchored to a hash, so removing needs a
			// fresh tree. That is the contract working, not a nuisance.
			tree2, err := config.Parse(config.ParseOptions{Path: root})
			require.NoError(t, err)

			var target *config.Node
			tree2.Walk(func(n *config.Node) bool {
				if target == nil && n.Directive == "server_name" {
					target = n
				}
				return true
			})
			require.NotNil(t, target)

			rm, err := ops.Remove(tree2, root, target.Ref)
			require.NoError(t, err)
			_, err = apply.Run(apply.Options{Plan: rm, Tree: tree2, Root: root,
				Validate: func() error { return nil }})
			require.NoError(t, err)

			back, err := os.ReadFile(root)
			require.NoError(t, err)
			require.Equal(t, src, string(back),
				"add then remove did not return the file to what it was")
		})
	}
}

// The indentation is copied from the file, not chosen. Asserted directly
// because the round trip above would also pass if both operations were
// consistently wrong.
func TestAddCopiesTheIndentationTheFileUses(t *testing.T) {
	cases := map[string]struct{ src, wantLine string }{
		"two spaces": {
			"events { worker_connections 16; }\nhttp {\n  server {\n    listen 8080;\n  }\n}\n",
			"    server_name a.test;",
		},
		"tabs": {
			"events { worker_connections 16; }\nhttp {\n\tserver {\n\t\tlisten 8080;\n\t}\n}\n",
			"\t\tserver_name a.test;",
		},
		"none": {
			"events { worker_connections 16; }\nhttp {\nserver {\nlisten 8080;\n}\n}\n",
			"server_name a.test;",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tree, root := parse(t, c.src)
			p, err := ops.Add(tree, root, refOf(t, tree, "server"), "server_name", []string{"a.test"})
			require.NoError(t, err)
			_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
				Validate: func() error { return nil }})
			require.NoError(t, err)

			got, err := os.ReadFile(root)
			require.NoError(t, err)
			require.Contains(t, strings.Split(string(got), "\n"), c.wantLine,
				"the inserted line does not match the file's own indentation:\n%s", got)
		})
	}
}

// A CRLF file stays CRLF. Converting a line ending is exactly the off-target
// change D1 exists to prevent, and it is invisible in a diff that ignores
// whitespace.
func TestAddKeepsCRLF(t *testing.T) {
	src := "events { worker_connections 16; }\r\nhttp {\r\n  server {\r\n    listen 8080;\r\n  }\r\n}\r\n"
	tree, root := parse(t, src)

	p, err := ops.Add(tree, root, refOf(t, tree, "server"), "server_name", []string{"a.test"})
	require.NoError(t, err)
	_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
		Validate: func() error { return nil }})
	require.NoError(t, err)

	got, err := os.ReadFile(root)
	require.NoError(t, err)
	require.Contains(t, string(got), "server_name a.test;\r\n")
	require.NotContains(t, strings.ReplaceAll(string(got), "\r\n", ""), "\n",
		"a bare LF was introduced into a CRLF file")
}

func TestRemoveTakesTheWholeLineAndLeavesBlankLinesAlone(t *testing.T) {
	src := "events { worker_connections 16; }\n" +
		"http {\n\n  server {\n    listen 8080;\n\n    server_name a.test;\n\n  }\n}\n"
	tree, root := parse(t, src)

	var target *config.Node
	tree.Walk(func(n *config.Node) bool {
		if target == nil && n.Directive == "server_name" {
			target = n
		}
		return true
	})
	require.NotNil(t, target)

	p, err := ops.Remove(tree, root, target.Ref)
	require.NoError(t, err)
	_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
		Validate: func() error { return nil }})
	require.NoError(t, err)

	got, err := os.ReadFile(root)
	require.NoError(t, err)
	require.Equal(t, "events { worker_connections 16; }\n"+
		"http {\n\n  server {\n    listen 8080;\n\n\n  }\n}\n", string(got),
		"the blank lines around the removed directive were disturbed")
	require.NotContains(t, string(got), "    \n", "an indented empty line was left behind")
}

// A directive that shares its line with something else keeps that line: taking
// the newline would join two lines that were not joined.
func TestRemoveDoesNotJoinLines(t *testing.T) {
	src := "events { worker_connections 16; }\nhttp { server { listen 8080; server_name a.test; } }\n"
	tree, root := parse(t, src)

	var target *config.Node
	tree.Walk(func(n *config.Node) bool {
		if target == nil && n.Directive == "server_name" {
			target = n
		}
		return true
	})
	require.NotNil(t, target)

	p, err := ops.Remove(tree, root, target.Ref)
	require.NoError(t, err)
	_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
		Validate: func() error { return nil }})
	require.NoError(t, err)

	got, err := os.ReadFile(root)
	require.NoError(t, err)
	require.Equal(t, strings.Count(src, "\n"), strings.Count(string(got), "\n"),
		"the number of lines changed:\n%s", got)
	require.NotContains(t, string(got), "server_name")

	// The spaces that surrounded the directive are still there, so the line now
	// has two in a row. That is deliberate: they were never part of the
	// directive, and consuming one to make the line tidier would be a change to
	// bytes nobody asked about. Faithful beats tidy in a file somebody owns.
	require.Equal(t, "events { worker_connections 16; }\n"+
		"http { server { listen 8080;  } }\n", string(got))
}

func TestAddRefuses(t *testing.T) {
	t.Run("a parent that opens no block", func(t *testing.T) {
		tree, root := parse(t, site)
		_, err := ops.Add(tree, root, refOf(t, tree, "listen"), "server_name", []string{"a"})
		requireRefusal(t, err, ops.CodeUnsupportedTarget)
	})

	t.Run("a parent that does not exist", func(t *testing.T) {
		tree, root := parse(t, site)
		_, err := ops.Add(tree, root, root+"#nope", "server_name", []string{"a"})
		requireRefusal(t, err, ops.CodeRefNotFound)
	})

	t.Run("no directive name", func(t *testing.T) {
		tree, root := parse(t, site)
		_, err := ops.Add(tree, root, refOf(t, tree, "server"), "", nil)
		requireRefusal(t, err, ops.CodeInvalidArguments)
	})
}

func TestRemoveRefusesARefThatNamesNothing(t *testing.T) {
	tree, root := parse(t, site)
	_, err := ops.Remove(tree, root, root+"#nope")
	requireRefusal(t, err, ops.CodeRefNotFound)
}
