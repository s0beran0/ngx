package cli_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The oracle of get is inspect, and this file is it.
//
// get and inspect answer questions about the same configuration, and the only
// thing keeping them from becoming two truths is that they read the same tree
// and hand out the same nodes. A get that built its own view -- flattening
// arguments, dropping a span, redacting on its own -- would pass every test
// written about get alone and disagree with inspect on the first real file.
//
// So the property is stated over BYTES, not over fields: the JSON of a node
// that comes out of get must be byte-identical to the JSON of that same node
// inside `inspect --full-tree`. Field-by-field comparison would be a second
// specification of the node, free to forget the field that was added last;
// bytes cannot forget one.
//
// The quantifier is exhaustive rather than random: every fixture in testdata,
// every directive name that occurs in it, and every --in and --value that the
// tree itself makes true. Generating random directive names would spend the
// run on names that match nothing, which the no-match tests already cover.

// nodeRef is one node of the inspect tree: its identity and its exact bytes.
type nodeRef struct {
	key       string
	directive string
	raw       json.RawMessage
}

// nodeShape is the little that has to be READ out of a node's JSON to index
// it. Everything else stays as raw bytes, which is the point.
type nodeShape struct {
	Directive string `json:"directive"`
	File      string `json:"file"`
	Args      []string
	Span      struct {
		Start int `json:"start"`
		End   int `json:"end"`
	} `json:"span"`
	Block []json.RawMessage `json:"block"`
}

// nodeKey identifies a node across the two commands. A byte range in a named
// file is unique by construction: two directives cannot start at the same
// offset of the same file.
func nodeKey(s nodeShape) string {
	return fmt.Sprintf("%s:%d-%d", s.File, s.Span.Start, s.Span.End)
}

func fixtures(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "*.conf"))
	require.NoError(t, err)
	// The nested layout is where the property is most at risk: --in has to
	// cross the include, and a node of an included file has to come out of
	// get exactly as inspect emits it.
	paths = append(paths, filepath.Join("testdata", "filters", "nginx.conf"))

	out := make([]string, 0, len(paths))
	for _, p := range paths {
		// A fixture that never yields a tree cannot say anything about two
		// commands agreeing on one: both of these are deliberately broken
		// and are covered by the tests about refusing them.
		switch filepath.Base(p) {
		case "invalid.conf", "if_empty.conf":
			continue
		}
		out = append(out, p)
	}
	return out
}

// inspectNodes indexes every node of the full tree by key, keeping the raw
// bytes of each one.
func inspectNodes(t *testing.T, path string) map[string]nodeRef {
	t.Helper()
	code, _, raw := runInspect(t, "inspect", "--full-tree", "-c", path)
	require.Equal(t, 0, int(code), "inspect failed for %s: %s", path, raw)

	var response struct {
		Data struct {
			Config []struct {
				Parsed []json.RawMessage `json:"parsed"`
			} `json:"config"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))

	index := map[string]nodeRef{}
	var walk func(nodes []json.RawMessage)
	walk = func(nodes []json.RawMessage) {
		for _, n := range nodes {
			var shape nodeShape
			require.NoError(t, json.Unmarshal(n, &shape))
			key := nodeKey(shape)
			_, repeated := index[key]
			require.False(t, repeated, "two nodes share the key %s", key)
			index[key] = nodeRef{key: key, directive: shape.Directive, raw: n}
			walk(shape.Block)
		}
	}
	for _, f := range response.Data.Config {
		walk(f.Parsed)
	}
	return index
}

// getMatches runs get and returns the raw bytes of each match, keyed the same
// way the inspect index is.
func getMatches(t *testing.T, args ...string) map[string]json.RawMessage {
	t.Helper()
	code, _, raw := runInspect(t, args...)
	require.Equal(t, 0, int(code), "get failed: %s", raw)

	var response struct {
		Data struct {
			Matches []json.RawMessage `json:"matches"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))

	out := map[string]json.RawMessage{}
	for _, m := range response.Data.Matches {
		var shape nodeShape
		require.NoError(t, json.Unmarshal(m, &shape))
		out[nodeKey(shape)] = m
	}
	require.Len(t, out, len(response.Data.Matches), "get returned the same node twice")
	return out
}

// TestGetMatchesAreByteIdenticalToTheInspectTree is the property itself: for
// every directive name of every fixture, get returns exactly the nodes that
// carry the name -- no more, none missing -- and each one byte for byte as
// inspect emits it.
func TestGetMatchesAreByteIdenticalToTheInspectTree(t *testing.T) {
	for _, path := range fixtures(t) {
		t.Run(path, func(t *testing.T) {
			tree := inspectNodes(t, path)
			require.NotEmpty(t, tree, "fixture with no node says nothing")

			for name := range directiveNames(tree) {
				expected := map[string]json.RawMessage{}
				for key, ref := range tree {
					if ref.directive == name {
						expected[key] = ref.raw
					}
				}

				matches := getMatches(t, "get", "--directive", name, "-c", path)

				require.Len(t, matches, len(expected),
					"--directive %q returned %d nodes and the tree holds %d", name, len(matches), len(expected))
				for key, want := range expected {
					got, ok := matches[key]
					require.True(t, ok, "--directive %q lost the node %s", name, key)
					// The comparison is over bytes, on purpose: JSONEq would
					// accept a node whose numbers were reformatted or whose
					// keys were reordered, and either one means get built the
					// node instead of handing over the one inspect has.
					require.Equal(t, string(want), string(got),
						"--directive %q returned %s differently from inspect", name, key)
				}
			}
		})
	}
}

// TestGetNarrowedStaysByteIdentical states the same property for the two
// flags that narrow: --in and --value can only ever REMOVE matches, never
// change one. Each case is derived from the tree itself, so the assertion is
// about a subset that is known to be non-empty.
func TestGetNarrowedStaysByteIdentical(t *testing.T) {
	for _, path := range fixtures(t) {
		t.Run(path, func(t *testing.T) {
			tree := inspectNodes(t, path)

			for _, block := range []string{"http", "server", "location", "upstream", "events", "stream"} {
				for name := range directiveNames(tree) {
					assertSubsetOfTree(t, tree,
						getMatches(t, "get", "--directive", name, "--in", block, "-c", path))
				}
			}

			for _, ref := range tree {
				if ref.directive == "#" {
					continue
				}
				var shape nodeShape
				require.NoError(t, json.Unmarshal(ref.raw, &shape))
				for _, arg := range shape.Args {
					assertSubsetOfTree(t, tree,
						getMatches(t, "get", "--directive", ref.directive, "--value", arg, "-c", path))
				}
			}
		})
	}
}

// directiveNames lists the names to quantify over, and it leaves out the one
// exception the property has: "#" is how a COMMENT appears in the tree, and
// get answers about directives, so it never returns comments. The exception is
// written here, in the oracle, instead of being absorbed silently by a
// comparison that happens to pass -- and TestGetNeverReturnsComments is what
// pins the behaviour down from the other side.
func directiveNames(tree map[string]nodeRef) map[string]bool {
	names := map[string]bool{}
	for _, ref := range tree {
		if ref.directive == "#" {
			continue
		}
		names[ref.directive] = true
	}
	return names
}

func assertSubsetOfTree(t *testing.T, tree map[string]nodeRef, matches map[string]json.RawMessage) {
	t.Helper()
	for key, got := range matches {
		ref, ok := tree[key]
		require.True(t, ok, "get returned a node the tree does not have: %s", key)
		require.Equal(t, string(ref.raw), string(got), "get returned %s differently from inspect", key)
	}
}
