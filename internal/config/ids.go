package config

import (
	"fmt"
	"strings"
)

// abbreviations shortens the most common block directives. A bare first letter
// would not do: server and stream would collide.
var abbreviations = map[string]string{
	"http":     "h",
	"stream":   "st",
	"events":   "e",
	"mail":     "m",
	"server":   "s",
	"location": "l",
	"upstream": "u",
	"map":      "mp",
}

// rootBlocks are the top-level contexts, which occur at most once and
// therefore need no index: the ID is "h", not "h0".
var rootBlocks = map[string]bool{
	"http":   true,
	"stream": true,
	"events": true,
	"mail":   true,
}

// AssignIDs fills in the ID field of every node, recursively.
//
// The index counts among siblings of the same directive, not by absolute
// position: inserting a location does not renumber the servers next to it.
// Comments get no ID and take no part in the count -- otherwise adding a
// comment would shift the IDs of the directives around it.
func AssignIDs(nodes []*Node, prefix string) {
	counters := map[string]int{}
	atRoot := prefix == ""

	for _, n := range nodes {
		if n.IsComment() {
			continue
		}

		seg := segment(n, counters, atRoot)
		if atRoot {
			n.ID = seg
		} else {
			n.ID = prefix + "." + seg
		}

		if len(n.Block) > 0 {
			AssignIDs(n.Block, n.ID)
		}
	}
}

func segment(n *Node, counters map[string]int, atRoot bool) string {
	// Only the root level goes without an index: a stream nested inside http
	// is just one more sibling block and has to be numbered like any other.
	if atRoot && n.HasBlock() && rootBlocks[n.Directive] {
		return abbreviate(n.Directive)
	}

	key := n.Directive
	base := abbreviate(n.Directive)
	if !n.HasBlock() && abbreviations[n.Directive] == "" {
		// Plain directives with no abbreviation of their own share the d
		// counter.
		key, base = "", "d"
	}

	i := counters[key]
	counters[key] = i + 1
	return fmt.Sprintf("%s%d", base, i)
}

func abbreviate(directive string) string {
	if a, ok := abbreviations[directive]; ok {
		return a
	}
	return directive
}

// FindByID locates a node by its ID. Returns nil when there is none, and also
// when there is more than one.
//
// Refusing the ambiguous case is not caution, it is the only safe answer
// available here. IDs are assigned per file (parse.go:121), so they are unique
// within a file and NOT within a configuration: on the standard layout, where
// every site is its own file under conf.d/*.conf, the first server of each
// file is "s0" and its first directive is "s0.d0". Measured against the test
// bench, 112 listen directives shared a single ID.
//
// Returning the first match would make a caller edit a node it never read,
// which is the silent error D3 exists to prevent. Whoever needs a node by ID
// today has the file that scopes it, because every output that carries an ID
// carries its file alongside.
//
// What the reference for a node across the whole configuration should be is a
// v0.2 decision and is recorded as a blocker in the implementation plan; it is
// not settled here.
func FindByID(t *Tree, id string) *Node {
	id = strings.TrimPrefix(id, "#")

	var found *Node
	ambiguous := false
	t.Walk(func(n *Node) bool {
		if n.ID != id {
			return true
		}
		if found != nil {
			ambiguous = true
			return false
		}
		found = n
		return true
	})
	if ambiguous {
		return nil
	}
	return found
}
