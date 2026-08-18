package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
)

// Combine resolves the includes, returning a single-file tree where every
// node carries its real origin.
//
// The resolution runs over our own tree rather than through crossplane's
// CombineConfigs because combining beforehand would destroy the spans: they
// point at offsets of specific files. Here the original nodes stay untouched
// and only the structure is rearranged.
func Combine(t *Tree) (*Tree, error) {
	if len(t.Files) == 0 {
		// Even when empty, the tree keeps the invariant that every Tree has
		// Hash filled in -- the same thing Parse guarantees.
		empty := &Tree{}
		empty.Hash = Hash(empty)
		return empty, nil
	}

	top := t.Files[0]
	c := &combiner{
		files:   t.Files,
		visited: map[string]bool{},
		// configDir is the directory of the top-level file, fixed for the
		// whole resolution -- the same approximation crossplane makes
		// (p.configDir in parse.go), which does not change as it descends
		// into included files. A relative pattern declared inside an
		// included file resolves against this directory, not against the
		// directory of the file that declared it.
		configDir: filepath.Dir(top.Path),
	}

	nodes, err := c.resolve(top)
	if err != nil {
		return nil, err
	}

	combined := &Tree{
		Files: []*File{{
			Path: top.Path,
			// Source is left empty on purpose: the combined tree is a
			// structural view, assembled from nodes of several files, and
			// each one carries Span/HeadSpan that only make sense against
			// the source of its own Origin.File. Whoever needs the text
			// resolves it through the original tree, using Origin to find
			// the real file.
			Source: nil,
			Nodes:  nodes,
		}},
	}
	AssignIDs(combined.Files[0].Nodes, "")
	combined.Hash = Hash(combined)
	return combined, nil
}

// files is a slice and not a map on purpose: an include with a glob may
// match several files, and iterating a map would give a different order on
// every run -- which would make the IDs and the hash change without the
// configuration changing.
type combiner struct {
	files     []*File
	visited   map[string]bool
	configDir string
}

func (c *combiner) resolve(f *File) ([]*Node, error) {
	if c.visited[f.Path] {
		return nil, fmt.Errorf("circular include detected in %s", f.Path)
	}
	c.visited[f.Path] = true
	defer delete(c.visited, f.Path)

	return c.expand(f.Nodes)
}

func (c *combiner) expand(nodes []*Node) ([]*Node, error) {
	var out []*Node

	for _, n := range nodes {
		if n.Directive == "include" {
			included, err := c.expandInclude(n)
			if err != nil {
				return nil, err
			}
			out = append(out, included...)
			continue
		}

		copied := *n
		// Args is cloned, not just copied by value: the shallow copy of *n
		// would leave Args pointing at the same backing array as the
		// original tree, and mutating one of them would affect the other --
		// exactly what cloneArgs in parse.go exists to prevent when "Task
		// 12 builds new nodes out of these ones".
		copied.Args = slices.Clone(n.Args)
		copied.Origin = &Origin{File: n.File, Line: n.Line}

		if len(n.Block) > 0 {
			children, err := c.expand(n.Block)
			if err != nil {
				return nil, err
			}
			copied.Block = children
		} else {
			// Without this, copied.Block would keep the slice header copied
			// from n.Block (empty, but potentially sharing the same backing
			// array): this way the copy is left with no tie to the original.
			copied.Block = nil
		}
		out = append(out, &copied)
	}

	return out, nil
}

// patternHasMagic matches the same characters crossplane uses to decide
// whether an include pattern is a glob (hasMagic in parse.go). A pattern with
// none of them is literal, and crossplane requires it to open and read
// successfully during the Parse -- so if it got here without matching any
// file of the tree, the bug is in our path comparison, not in the
// configuration.
var patternHasMagic = regexp.MustCompile(`[*?[]`)

// expandInclude locates the files that match the include pattern.
// Crossplane has already resolved the globs and returned each matched file as
// a Config of its own, so all that is left is finding the ones not consumed yet.
func (c *combiner) expandInclude(n *Node) ([]*Node, error) {
	matches := c.filesForInclude(n)

	if len(matches) == 0 && len(n.Args) > 0 && !patternHasMagic.MatchString(n.Args[0]) {
		return nil, fmt.Errorf(
			"literal include %q at %s:%d matched no file of the tree",
			n.Args[0], n.File, n.Line,
		)
	}

	var out []*Node
	for _, target := range matches {
		nodes, err := c.resolve(target)
		if err != nil {
			return nil, err
		}
		out = append(out, nodes...)
	}

	return out, nil
}

// The iteration runs over the slice of files, in the order crossplane
// returned them, so that the result is deterministic.
//
// Only Args[0] takes part in the comparison: it is the only argument
// crossplane uses to resolve the include (stmt.Args[0] in parse.go);
// considering the rest would risk matching files crossplane never treated as
// included by that node. An include with no arguments already fails during
// Parse, but the guard keeps us from indexing an empty slice should one ever
// reach here hand-built.
func (c *combiner) filesForInclude(n *Node) []*File {
	if len(n.Args) == 0 {
		return nil
	}
	pattern := n.Args[0]

	var matches []*File
	for _, f := range c.files {
		if matchesInclude(f.Path, pattern, c.configDir) {
			matches = append(matches, f)
		}
	}
	return matches
}

// matchesInclude decides whether a parsed file corresponds to the pattern of an
// include, mirroring crossplane's resolution (parse.go): a relative pattern
// is joined with configDir -- the directory of the top-level file, fixed for
// the whole parse -- never with the directory of whoever declared the include.
//
// Once resolved, the comparison is either by equality (the common case, a
// literal pattern pointing exactly at one File.Path) or by filepath.Match (a
// pattern with a glob). There is no third branch comparing the raw pattern:
// that would open the door to matching a resolved path against a different base.
func matchesInclude(path, pattern, configDir string) bool {
	resolved := pattern
	if !filepath.IsAbs(pattern) {
		resolved = filepath.Join(configDir, pattern)
	}

	if path == resolved {
		return true
	}
	if ok, _ := filepath.Match(resolved, path); ok {
		return true
	}
	return false
}
