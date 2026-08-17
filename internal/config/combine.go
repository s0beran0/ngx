package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
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
		// Mesmo vazia, a arvore mantem a invariante de que todo Tree tem
		// Hash preenchido -- e o que Parse tambem garante.
		vazia := &Tree{}
		vazia.Hash = Hash(vazia)
		return vazia, nil
	}

	principal := t.Files[0]
	c := &combinador{
		arquivos:  t.Files,
		visitados: map[string]bool{},
		// configDir e o diretorio do arquivo de topo, fixo para a resolucao
		// inteira -- e a mesma aproximacao que o crossplane usa (p.configDir
		// em parse.go), que nao muda ao descer para arquivos incluidos. Um
		// padrao relativo declarado dentro de um arquivo incluido resolve
		// contra este diretorio, nao contra o diretorio de quem declarou.
		configDir: filepath.Dir(principal.Path),
	}

	nodes, err := c.resolver(principal)
	if err != nil {
		return nil, err
	}

	combinado := &Tree{
		Files: []*File{{
			Path: principal.Path,
			// Source fica vazio de proposito: a arvore combinada e uma view
			// estrutural, montada com nos de varios arquivos, e cada um
			// carrega Span/HeadSpan que so fazem sentido contra a fonte do
			// seu proprio Origin.File. Quem precisar do texto resolve pela
			// arvore original, usando Origin para achar o arquivo real.
			Source: nil,
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
	configDir string
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
		// Args e clonado, nao apenas copiado por valor: a copia rasa de *n
		// deixaria Args apontando para o mesmo array de backing da arvore
		// original, e mutar um dos dois afetaria o outro -- exatamente o
		// que clonarArgs em parse.go existe para evitar quando "a Task 12
		// monta nos novos a partir destes".
		copia.Args = slices.Clone(n.Args)
		copia.Origin = &Origin{File: n.File, Line: n.Line}

		if len(n.Block) > 0 {
			filhos, err := c.expandir(n.Block)
			if err != nil {
				return nil, err
			}
			copia.Block = filhos
		} else {
			// Sem isso, copia.Block manteria o slice header copiado de
			// n.Block (vazio, mas potencialmente com o mesmo array de
			// backing): a copia ficaria sem nenhum laco com a original.
			copia.Block = nil
		}
		saida = append(saida, &copia)
	}

	return saida, nil
}

// padraoTemMagic casa os mesmos caracteres que o crossplane usa para decidir
// se um padrao de include e um glob (hasMagic em parse.go). Um padrao sem
// nenhum deles e literal, e o crossplane exige que ele abra e leia com
// sucesso durante o Parse -- se chegou aqui sem casar nenhum arquivo da
// arvore, o bug esta na nossa comparacao de caminhos, nao na configuracao.
var padraoTemMagic = regexp.MustCompile(`[*?[]`)

// expandirInclude localiza os arquivos que casam com o padrao do include.
// O crossplane ja resolveu os globs e devolveu cada arquivo casado como um
// Config proprio, entao basta encontrar os que ainda nao foram consumidos.
func (c *combinador) expandirInclude(n *Node) ([]*Node, error) {
	achados := c.arquivosDoInclude(n)

	if len(achados) == 0 && len(n.Args) > 0 && !padraoTemMagic.MatchString(n.Args[0]) {
		return nil, fmt.Errorf(
			"include literal %q em %s:%d nao casou nenhum arquivo da arvore",
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

// A iteracao e sobre a slice de arquivos, na ordem em que o crossplane os
// devolveu, para que o resultado seja deterministico.
//
// So Args[0] entra na comparacao: e o unico argumento que o crossplane usa
// para resolver o include (stmt.Args[0] em parse.go); considerar os demais
// arriscaria casar arquivos que o crossplane nunca tratou como incluidos
// por aquele no. Um include sem argumentos ja falha no Parse, mas a guarda
// evita indexar uma slice vazia se algum dia chegar aqui montado a mao.
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

// casaInclude decide se um arquivo parseado corresponde ao padrao de um
// include, espelhando a resolucao do crossplane (parse.go): um padrao
// relativo junta com configDir -- o diretorio do arquivo de topo, fixo para
// o parse inteiro -- nunca com o diretorio de quem declarou o include.
//
// Depois de resolvido, a comparacao e por igualdade (o caso comum, padrao
// literal apontando exatamente para um File.Path) ou por filepath.Match
// (padrao com glob). Nao ha um terceiro ramo comparando o padrao cru: isso
// abriria a porta para casar um caminho resolvido contra outra base.
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
