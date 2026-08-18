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

// FindByID locates a node by its ID. Returns nil when there is none.
func FindByID(t *Tree, id string) *Node {
	id = strings.TrimPrefix(id, "#")

	var found *Node
	t.Walk(func(n *Node) bool {
		if found != nil {
			return false
		}
		if n.ID == id {
			found = n
			return false
		}
		return true
	})
	return found
}
