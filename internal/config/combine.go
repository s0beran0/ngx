package config

import (
	"fmt"
	"path/filepath"
)

// Combine resolve os includes, devolvendo uma arvore de arquivo unico onde
// cada no carrega a origem real.
//
// A resolucao e feita sobre a nossa arvore, e nao pelo CombineConfigs do
// crossplane, porque combinar antes destruiria os spans: eles apontam para
// offsets de arquivos especificos. Aqui os nos originais permanecem intactos
// e apenas a estrutura e reorganizada.
func Combine(t *Tree) (*Tree, error) {
	if len(t.Files) == 0 {
		return &Tree{}, nil
	}

	principal := t.Files[0]
	c := &combinador{arquivos: t.Files, visitados: map[string]bool{}}

	nodes, err := c.resolver(principal)
	if err != nil {
		return nil, err
	}

	combinado := &Tree{
		Files: []*File{{
			Path:   principal.Path,
			Source: principal.Source,
			Nodes:  nodes,
		}},
	}
	AtribuirIDs(combinado.Files[0].Nodes, "")
	combinado.Hash = Hash(combinado)
	return combinado, nil
}

// arquivos e uma slice, e nao um map, de proposito: um include com glob pode
// casar varios arquivos, e iterar um map daria ordem diferente a cada
// execucao — o que faria os IDs e o hash mudarem sem a configuracao mudar.
type combinador struct {
	arquivos  []*File
	visitados map[string]bool
}

func (c *combinador) resolver(f *File) ([]*Node, error) {
	if c.visitados[f.Path] {
		return nil, fmt.Errorf("include circular detectado em %s", f.Path)
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
		copia.Origin = &Origin{File: n.File, Line: n.Line}

		if len(n.Block) > 0 {
			filhos, err := c.expandir(n.Block)
			if err != nil {
				return nil, err
			}
			copia.Block = filhos
		}
		saida = append(saida, &copia)
	}

	return saida, nil
}

// expandirInclude localiza os arquivos que casam com o padrao do include.
// O crossplane ja resolveu os globs e devolveu cada arquivo casado como um
// Config proprio, entao basta encontrar os que ainda nao foram consumidos.
func (c *combinador) expandirInclude(n *Node) ([]*Node, error) {
	var saida []*Node

	for _, alvo := range c.arquivosDoInclude(n) {
		nodes, err := c.resolver(alvo)
		if err != nil {
			return nil, err
		}
		saida = append(saida, nodes...)
	}

	return saida, nil
}

// A iteracao e sobre a slice de arquivos, na ordem em que o crossplane os
// devolveu, para que o resultado seja deterministico.
func (c *combinador) arquivosDoInclude(n *Node) []*File {
	var achados []*File
	for _, f := range c.arquivos {
		for _, arg := range n.Args {
			if casaInclude(f.Path, arg, n.File) {
				achados = append(achados, f)
				break
			}
		}
	}
	return achados
}

// casaInclude decide se um arquivo parseado corresponde ao padrao de um
// include. O padrao pode ser relativo ao arquivo que o declarou.
func casaInclude(caminho, padrao, declaradoEm string) bool {
	if caminho == padrao {
		return true
	}
	base := filepath.Dir(declaradoEm)
	if ok, _ := filepath.Match(filepath.Join(base, padrao), caminho); ok {
		return true
	}
	if ok, _ := filepath.Match(padrao, caminho); ok {
		return true
	}
	return false
}
