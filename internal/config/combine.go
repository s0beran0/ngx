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
		vazia := &Tree{}
		vazia.Hash = Hash(vazia)
		return vazia, nil
	}

	principal := t.Files[0]
	c := &combinador{
		arquivos:  t.Files,
		visitados: map[string]bool{},
		// configDir is the directory of the top-level file, fixed for the
		// whole resolution -- the same approximation crossplane makes
		// (p.configDir in parse.go), which does not change as it descends
		// into included files. A relative pattern declared inside an
		// included file resolves against this directory, not against the
		// directory of the file that declared it.
		configDir: filepath.Dir(principal.Path),
	}

	nodes, err := c.resolver(principal)
	if err != nil {
		return nil, err
	}

	combinado := &Tree{
		Files: []*File{{
			Path: principal.Path,
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
	AtribuirIDs(combinado.Files[0].Nodes, "")
	combinado.Hash = Hash(combinado)
	return combinado, nil
}

// arquivos is a slice and not a map on purpose: an include with a glob may
// match several files, and iterating a map would give a different order on
// every run -- which would make the IDs and the hash change without the
// configuration changing.
type combinador struct {
	arquivos  []*File
	visitados map[string]bool
	configDir string
}

func (c *combinador) resolver(f *File) ([]*Node, error) {
	if c.visitados[f.Path] {
		return nil, fmt.Errorf("circular include detected in %s", f.Path)
	}
	c.visitados[f.Path] = true
	defer delete(c.visitados, f.Path)

	return c.expandir(f.Nodes)
}

func (c *combinador) expandir(nodes []*Node) ([]*Node, error) {
	var saida []*Node

	for _, n := range nodes {
		if n.Directive == "include" {
			incluidos, err := c.expandirInclude(n)
			if err != nil {
				return nil, err
			}
			saida = append(saida, incluidos...)
			continue
		}

		copia := *n
		// Args is cloned, not just copied by value: the shallow copy of *n
		// would leave Args pointing at the same backing array as the
		// original tree, and mutating one of them would affect the other --
		// exactly what clonarArgs in parse.go exists to prevent when "Task
		// 12 builds new nodes out of these ones".
		copia.Args = slices.Clone(n.Args)
		copia.Origin = &Origin{File: n.File, Line: n.Line}

		if len(n.Block) > 0 {
			filhos, err := c.expandir(n.Block)
			if err != nil {
				return nil, err
			}
			copia.Block = filhos
		} else {
			// Without this, copia.Block would keep the slice header copied
			// from n.Block (empty, but potentially sharing the same backing
			// array): this way the copy is left with no tie to the original.
			copia.Block = nil
		}
		saida = append(saida, &copia)
	}

	return saida, nil
}

// padraoTemMagic matches the same characters crossplane uses to decide
// whether an include pattern is a glob (hasMagic in parse.go). A pattern with
// none of them is literal, and crossplane requires it to open and read
// successfully during the Parse -- so if it got here without matching any
// file of the tree, the bug is in our path comparison, not in the
// configuration.
var padraoTemMagic = regexp.MustCompile(`[*?[]`)

// expandirInclude locates the files that match the include pattern.
// Crossplane has already resolved the globs and returned each matched file as
// a Config of its own, so all that is left is finding the ones not consumed yet.
func (c *combinador) expandirInclude(n *Node) ([]*Node, error) {
	achados := c.arquivosDoInclude(n)

	if len(achados) == 0 && len(n.Args) > 0 && !padraoTemMagic.MatchString(n.Args[0]) {
		return nil, fmt.Errorf(
			"literal include %q at %s:%d matched no file of the tree",
			n.Args[0], n.File, n.Line,
		)
	}

	var saida []*Node
	for _, alvo := range achados {
		nodes, err := c.resolver(alvo)
		if err != nil {
			return nil, err
		}
		saida = append(saida, nodes...)
	}

	return saida, nil
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
func (c *combinador) arquivosDoInclude(n *Node) []*File {
	if len(n.Args) == 0 {
		return nil
	}
	padrao := n.Args[0]

	var achados []*File
	for _, f := range c.arquivos {
		if casaInclude(f.Path, padrao, c.configDir) {
			achados = append(achados, f)
		}
	}
	return achados
}

// casaInclude decides whether a parsed file corresponds to the pattern of an
// include, mirroring crossplane's resolution (parse.go): a relative pattern
// is joined with configDir -- the directory of the top-level file, fixed for
// the whole parse -- never with the directory of whoever declared the include.
//
// Once resolved, the comparison is either by equality (the common case, a
// literal pattern pointing exactly at one File.Path) or by filepath.Match (a
// pattern with a glob). There is no third branch comparing the raw pattern:
// that would open the door to matching a resolved path against a different base.
func casaInclude(caminho, padrao, configDir string) bool {
	resolvido := padrao
	if !filepath.IsAbs(padrao) {
		resolvido = filepath.Join(configDir, padrao)
	}

	if caminho == resolvido {
		return true
	}
	if ok, _ := filepath.Match(resolvido, caminho); ok {
		return true
	}
	return false
}
