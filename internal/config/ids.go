package config

import (
	"fmt"
	"strings"
)

// abreviacoes encurta as diretivas de bloco mais comuns. Primeira letra
// sozinha nao serve: server e stream colidiriam.
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

// blocosRaiz sao os contextos de topo, que ocorrem no maximo uma vez e por
// isso dispensam indice: o ID e "h", nao "h0".
var blocosRaiz = map[string]bool{
	"http":   true,
	"stream": true,
	"events": true,
	"mail":   true,
}

// AtribuirIDs preenche o campo ID de cada no, recursivamente.
//
// O indice conta entre irmaos da mesma diretiva, nao por posicao absoluta:
// inserir uma location nao renumera os servers ao lado. Comentarios nao
// recebem ID nem participam da contagem, senao adicionar um comentario
// deslocaria os IDs das diretivas vizinhas.
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
	// So o nivel raiz dispensa indice: um stream aninhado dentro de http e
	// apenas mais um bloco irmao e precisa ser numerado normalmente.
	if naRaiz && n.HasBlock() && blocosRaiz[n.Directive] {
		return abreviar(n.Directive)
	}

	chave := n.Directive
	base := abreviar(n.Directive)
	if !n.HasBlock() && abreviacoes[n.Directive] == "" {
		// Diretivas simples sem abreviacao propria compartilham o contador d.
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

// FindByID localiza um no pelo seu ID. Devolve nil se nao existir.
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
