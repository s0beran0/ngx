package config

import (
	"fmt"
	"strings"
)

// abreviacoes shortens the most common block directives. A bare first letter
// would not do: server and stream would collide.
var abreviacoes = map[string]string{
	"http":     "h",
	"stream":   "st",
	"events":   "e",
	"mail":     "m",
	"server":   "s",
	"location": "l",
	"upstream": "u",
	"map":      "mp",
}

// blocosRaiz are the top-level contexts, which occur at most once and
// therefore need no index: the ID is "h", not "h0".
var blocosRaiz = map[string]bool{
	"http":   true,
	"stream": true,
	"events": true,
	"mail":   true,
}

// AtribuirIDs fills in the ID field of every node, recursively.
//
// The index counts among siblings of the same directive, not by absolute
// position: inserting a location does not renumber the servers next to it.
// Comments get no ID and take no part in the count -- otherwise adding a
// comment would shift the IDs of the directives around it.
func AtribuirIDs(nodes []*Node, prefixo string) {
	contadores := map[string]int{}
	naRaiz := prefixo == ""

	for _, n := range nodes {
		if n.IsComment() {
			continue
		}

		seg := segmento(n, contadores, naRaiz)
		if naRaiz {
			n.ID = seg
		} else {
			n.ID = prefixo + "." + seg
		}

		if len(n.Block) > 0 {
			AtribuirIDs(n.Block, n.ID)
		}
	}
}

func segmento(n *Node, contadores map[string]int, naRaiz bool) string {
	// Only the root level goes without an index: a stream nested inside http
	// is just one more sibling block and has to be numbered like any other.
	if naRaiz && n.HasBlock() && blocosRaiz[n.Directive] {
		return abreviar(n.Directive)
	}

	chave := n.Directive
	base := abreviar(n.Directive)
	if !n.HasBlock() && abreviacoes[n.Directive] == "" {
		// Plain directives with no abbreviation of their own share the d
		// counter.
		chave, base = "", "d"
	}

	i := contadores[chave]
	contadores[chave] = i + 1
	return fmt.Sprintf("%s%d", base, i)
}

func abreviar(directive string) string {
	if a, ok := abreviacoes[directive]; ok {
		return a
	}
	return directive
}

// FindByID locates a node by its ID. Returns nil when there is none.
func FindByID(t *Tree, id string) *Node {
	id = strings.TrimPrefix(id, "#")

	var achado *Node
	t.Walk(func(n *Node) bool {
		if achado != nil {
			return false
		}
		if n.ID == id {
			achado = n
			return false
		}
		return true
	})
	return achado
}
