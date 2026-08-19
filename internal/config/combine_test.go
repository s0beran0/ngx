package config_test

import (
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func parseCombine(t *testing.T) *config.Tree {
	t.Helper()
	tree, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "combine", "nginx.conf"),
	})
	require.NoError(t, err)
	return tree
}

func TestParseWithoutCombineKeepsFilesSeparate(t *testing.T) {
	tree := parseCombine(t)

	require.Len(t, tree.Files, 3, "nginx.conf, conf.d/api.conf and snippets/proxy.conf")
}

func TestCombineProducesASingleFile(t *testing.T) {
	combined, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	require.Len(t, combined.Files, 1)
}

func TestCombineReplacesIncludeWithIncludedNodes(t *testing.T) {
	combined, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var http *config.Node
	combined.Walk(func(n *config.Node) bool {
		if n.Directive == "http" {
			http = n
			return false
		}
		return true
	})
	require.NotNil(t, http)

	var names []string
	for _, child := range http.Block {
		names = append(names, child.Directive)
	}
	require.Equal(t, []string{"server", "server"}, names,
		"the include is gone and became the server of the included file")
}

// Origin is what lets the agent know which real file to edit after seeing the
// resolved configuration.
func TestCombineFillsOriginWithTheRealFile(t *testing.T) {
	combined, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var api *config.Node
	combined.Walk(func(n *config.Node) bool {
		if n.Directive == "server_name" && len(n.Args) > 0 && n.Args[0] == "api.example.com" {
			api = n
			return false
		}
		return true
	})
	require.NotNil(t, api)

	require.NotNil(t, api.Origin)
	require.Contains(t, api.Origin.File, "api.conf")
	require.Greater(t, api.Origin.Line, 0)
}

func TestCombineKeepsOriginOfTopFile(t *testing.T) {
	combined, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var legacy *config.Node
	combined.Walk(func(n *config.Node) bool {
		if n.Directive == "server_name" && len(n.Args) > 0 && n.Args[0] == "legacy.example.com" {
			legacy = n
			return false
		}
		return true
	})
	require.NotNil(t, legacy)

	require.NotNil(t, legacy.Origin)
	require.Contains(t, legacy.Origin.File, "nginx.conf")
}

// The IDs of the combined tree are renumbered over the resolved structure:
// that is the structure the agent sees and the one it operates on.
func TestCombineRenumbersIDsOverResolvedStructure(t *testing.T) {
	combined, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	api := config.FindByID(combined, "h.s0")
	require.NotNil(t, api)
	require.Equal(t, "server", api.Directive)
	require.Contains(t, api.Origin.File, "api.conf",
		"the first server of the resolved tree comes from the include")

	legacy := config.FindByID(combined, "h.s1")
	require.NotNil(t, legacy)
	require.Contains(t, legacy.Origin.File, "nginx.conf")
}

// The hash of the combined tree differs from the uncombined one: they are
// different views, and conflating them would invalidate IDs for no reason.
func TestCombineRecomputesTheHash(t *testing.T) {
	original := parseCombine(t)
	combined, err := config.Combine(original)
	require.NoError(t, err)

	require.NotEmpty(t, combined.Hash)
	require.NotEqual(t, original.Hash, combined.Hash)
}

// Include nested two levels deep: nginx.conf includes conf.d/api.conf, which
// in turn includes snippets/proxy.conf. The relative pattern declared inside
// conf.d/api.conf resolves against the directory of the top-level file
// (nginx.conf), not against the directory of whoever declared the include --
// the same rule crossplane uses (p.configDir, fixed for the whole parse).
// Standard Debian layout: /etc/nginx/conf.d/*.conf including something in
// /etc/nginx/snippets/, not in /etc/nginx/conf.d/snippets/.
func TestCombineResolvesNestedIncludeTwoLevels(t *testing.T) {
	combined, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var proxy *config.Node
	combined.Walk(func(n *config.Node) bool {
		if n.Directive == "proxy_pass" {
			proxy = n
			return false
		}
		return true
	})
	require.NotNil(t, proxy,
		"the content of the third file (nested include) has to show up in the combined tree")

	require.NotNil(t, proxy.Origin)
	require.Contains(t, proxy.Origin.File, "proxy.conf")
}

// A literal include (no *, ? or [) that matches no file of the tree means a
// bug in our path comparison: Parse already fails loudly when crossplane
// cannot open a literal include, so this case should never survive silently
// all the way to Combine.
func TestCombineLiteralIncludeWithNoMatchingFileFails(t *testing.T) {
	topPath := filepath.Join("testdata", "combine", "nginx.conf")
	tree := &config.Tree{
		Files: []*config.File{
			{
				Path: topPath,
				Nodes: []*config.Node{
					{
						Directive: "include",
						Args:      []string{"does-not-exist.conf"},
						File:      topPath,
						Line:      3,
					},
				},
			},
		},
	}

	_, err := config.Combine(tree)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does-not-exist.conf")
}

// Args is cloned in the copy: mutating the combined tree must not change the
// original tree, because both stay alive at the same time (the original holds
// the real spans used for editing).
func TestCombineDoesNotShareArgsWithOriginalTree(t *testing.T) {
	original := parseCombine(t)
	combined, err := config.Combine(original)
	require.NoError(t, err)

	findLegacy := func(t *config.Tree) *config.Node {
		var found *config.Node
		t.Walk(func(n *config.Node) bool {
			if n.Directive == "server_name" && len(n.Args) > 0 && n.Args[0] == "legacy.example.com" {
				found = n
				return false
			}
			return true
		})
		return found
	}

	legacyOriginal := findLegacy(original)
	require.NotNil(t, legacyOriginal)

	legacyCombined := findLegacy(combined)
	require.NotNil(t, legacyCombined)

	legacyCombined.Args[0] = "mutated.invalid"
	require.Equal(t, "legacy.example.com", legacyOriginal.Args[0],
		"mutating Args of the combined tree must not affect the original tree")
}

// The nesting of nginx does not live inside one file: on the layout every
// distribution ships, the "http" that contains a server block is written in
// nginx.conf and the block lives in a file it includes. IncludeAncestors is
// what lets a question about the nesting be answered without combining the
// tree -- combining loses the source text and the per-file spans with it.
func TestIncludeAncestorsCrossesTheIncludes(t *testing.T) {
	ancestors := config.IncludeAncestors(parseCombine(t))

	require.Equal(t, []string{"http"},
		ancestors[filepath.Join("testdata", "combine", "conf.d", "api.conf")])
	// Two levels down: the snippet is included by a server that the http of
	// the top-level file contains, so both names apply to it.
	require.Equal(t, []string{"http", "server"},
		ancestors[filepath.Join("testdata", "combine", "snippets", "proxy.conf")])
}

// The top-level file is enclosed by nothing, and saying otherwise would be
// inventing a block that is not written anywhere.
func TestIncludeAncestorsLeavesTheTopFileOut(t *testing.T) {
	ancestors := config.IncludeAncestors(parseCombine(t))

	_, present := ancestors[filepath.Join("testdata", "combine", "nginx.conf")]
	require.False(t, present)
}

// An empty tree yields an empty map and not a nil one, for the reason every
// list in this project serializes as []: a caller that indexes the result
// should not have to check first.
func TestIncludeAncestorsOfAnEmptyTree(t *testing.T) {
	require.NotNil(t, config.IncludeAncestors(&config.Tree{}))
}
