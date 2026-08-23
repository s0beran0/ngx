package ops_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/apply"
	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/ops"
	"github.com/s0beran0/ngx/internal/plan"
)

// The diff of every operation contains ONLY the lines it meant to touch.
//
// "The file is correct" and "the diff is clean" are different properties, and
// the second is the one a reviewer sees. A change that reindents a neighbouring
// line, converts a line ending or reorders a block can still produce a file
// nginx accepts -- and a diff nobody wants to approve.
//
// The comparison is line by line against the ORIGINAL, so a change anywhere
// else shows up as an extra pair whatever its cause.
func diffLines(t *testing.T, before, after string) (added, removed []string) {
	t.Helper()

	b := strings.Split(before, "\n")
	a := strings.Split(after, "\n")

	inB := map[string]int{}
	for _, l := range b {
		inB[l]++
	}
	for _, l := range a {
		if inB[l] > 0 {
			inB[l]--
			continue
		}
		added = append(added, l)
	}

	inA := map[string]int{}
	for _, l := range a {
		inA[l]++
	}
	for _, l := range b {
		if inA[l] > 0 {
			inA[l]--
			continue
		}
		removed = append(removed, l)
	}
	return added, removed
}

const diffSite = "events { worker_connections 16; }\n" +
	"http {\n" +
	"\n" +
	"  # the public site\n" +
	"  server {\n" +
	"    listen 8080;\n" +
	"    server_name a.test;\n" +
	"\n" +
	"    add_header X-A \"b; c\";\n" +
	"  }\n" +
	"}\n"

func TestSetTouchesExactlyOneLine(t *testing.T) {
	tree, root := parse(t, diffSite)

	p, err := ops.Set(tree, root, refOf(t, tree, "listen"), []string{"8443"})
	require.NoError(t, err)
	_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
		Validate: func() error { return nil }})
	require.NoError(t, err)

	after, err := os.ReadFile(root)
	require.NoError(t, err)

	added, removed := diffLines(t, diffSite, string(after))
	require.Equal(t, []string{"    listen 8443;"}, added)
	require.Equal(t, []string{"    listen 8080;"}, removed)
}

func TestAddTouchesExactlyOneLine(t *testing.T) {
	tree, root := parse(t, diffSite)

	p, err := ops.Add(tree, root, refOf(t, tree, "server"), "server_tokens", []string{"off"})
	require.NoError(t, err)
	_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
		Validate: func() error { return nil }})
	require.NoError(t, err)

	after, err := os.ReadFile(root)
	require.NoError(t, err)

	added, removed := diffLines(t, diffSite, string(after))
	require.Equal(t, []string{"    server_tokens off;"}, added,
		"add touched more than the line it inserted")
	require.Empty(t, removed, "add removed lines")
}

func TestRemoveTouchesExactlyOneLine(t *testing.T) {
	tree, root := parse(t, diffSite)

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

	after, err := os.ReadFile(root)
	require.NoError(t, err)

	added, removed := diffLines(t, diffSite, string(after))
	require.Empty(t, added, "remove added lines")
	require.Equal(t, []string{"    server_name a.test;"}, removed)
}

// The comment and the blank lines are the ones most easily disturbed, so they
// get their own assertion: still there, in the same number, after each
// operation.
func TestNoOperationDisturbsCommentsOrBlankLines(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		tree, root := parse(t, diffSite)
		p, err := ops.Set(tree, root, refOf(t, tree, "listen"), []string{"8443"})
		require.NoError(t, err)
		requireUndisturbed(t, tree, root, p)
	})

	t.Run("add", func(t *testing.T) {
		tree, root := parse(t, diffSite)
		p, err := ops.Add(tree, root, refOf(t, tree, "server"), "server_tokens", []string{"off"})
		require.NoError(t, err)
		requireUndisturbed(t, tree, root, p)
	})

	t.Run("remove", func(t *testing.T) {
		tree, root := parse(t, diffSite)
		var target *config.Node
		tree.Walk(func(n *config.Node) bool {
			if target == nil && n.Directive == "add_header" {
				target = n
			}
			return true
		})
		require.NotNil(t, target)
		p, err := ops.Remove(tree, root, target.Ref)
		require.NoError(t, err)
		requireUndisturbed(t, tree, root, p)
	})
}

func requireUndisturbed(t *testing.T, tree *config.Tree, root string, p *plan.Plan) {
	t.Helper()

	_, err := apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
		Validate: func() error { return nil }})
	require.NoError(t, err)

	after, err := os.ReadFile(root)
	require.NoError(t, err)

	require.Equal(t, strings.Count(diffSite, "# the public site"),
		strings.Count(string(after), "# the public site"), "the comment was disturbed")
	require.Equal(t, strings.Count(diffSite, "\n\n"),
		strings.Count(string(after), "\n\n"),
		"the blank lines changed:\n%s", after)
}
