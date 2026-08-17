// Package config e a representacao canonica da configuracao do nginx: a
// arvore semantica vem do nginx-go-crossplane, os offsets de byte vem do
// tokenizador deste pacote, e as duas sao casadas por sequencia de tokens.
package config

// Span e um intervalo de bytes no arquivo de origem, com End exclusivo.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Len devolve o tamanho do intervalo em bytes.
func (s Span) Len() int { return s.End - s.Start }

// Origin registra de onde um no veio depois de resolver include.
type Origin struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Node e uma diretiva. Span cobre a diretiva inteira, incluindo o bloco e o
// delimitador final; HeadSpan cobre apenas o nome e os argumentos. Ter os
// dois e o que torna a edicao da v0.2 uma substituicao de bytes em vez de
// uma re-renderizacao do arquivo.
type Node struct {
	Directive string   `json:"directive"`
	Args      []string `json:"args"`
	File      string   `json:"file,omitempty"`
	Line      int      `json:"line"`
	Column    int      `json:"column"`
	Span      Span     `json:"span"`
	HeadSpan  Span     `json:"head_span"`

	// HeadComments sao os spans dos comentarios que caem DENTRO de HeadSpan
	// -- "default # x\n 0;" tem o comentario entre o nome e o ultimo
	// argumento. O crossplane tira esses comentarios de Args
	// (parse.go:286-290), entao sem este campo eles ficariam invisiveis na
	// arvore e a reescrita da v0.2, que substitui os bytes de HeadSpan,
	// apagaria um comentario que o usuario escreveu. Vazio na esmagadora
	// maioria dos nos, por isso omitempty.
	HeadComments []Span  `json:"head_comments,omitempty"`
	ID           string  `json:"id,omitempty"`
	Comment      *string `json:"comment,omitempty"`
	Block        []*Node `json:"block,omitempty"`
	Origin       *Origin `json:"origin,omitempty"`

	// temBloco distingue "server {}" de "server;". O campo Block nao serve
	// para isso: um bloco vazio e uma slice vazia, indistinguivel de nil
	// depois da serializacao.
	temBloco bool
}

// IsComment informa se o no representa um comentario.
//
// Directive == "#" sozinho nao basta: uma diretiva cujo NOME e o texto
// citado "#" tambem chega aqui com Directive == "#", e ela e uma diretiva de
// verdade, com argumentos e ate com bloco. O crossplane faz a distincao com
// !IsQuoted (parse.go:264) e so preenche Comment nos dois caminhos que sao
// comentario de fato (parse.go:264-268 e parse.go:438-444); Comment != nil e
// portanto o mesmo teste, do lado de ca.
func (n *Node) IsComment() bool { return n.Directive == "#" && n.Comment != nil }

// HasBlock informa se o no abre um bloco, inclusive vazio.
func (n *Node) HasBlock() bool { return n.temBloco }

// File e um arquivo de configuracao com sua fonte original preservada. A
// fonte e necessaria para que os spans possam ser resolvidos em texto.
type File struct {
	Path   string  `json:"file"`
	Source []byte  `json:"-"`
	Nodes  []*Node `json:"parsed"`
}

// Tree e o resultado completo de um parse.
type Tree struct {
	Files []*File `json:"config"`
	Hash  string  `json:"-"`
}

// Walk percorre a arvore em pre-ordem. Se fn devolver false, os filhos
// daquele no sao pulados.
func (t *Tree) Walk(fn func(*Node) bool) {
	for _, f := range t.Files {
		walkNodes(f.Nodes, fn)
	}
}

func walkNodes(nodes []*Node, fn func(*Node) bool) {
	for _, n := range nodes {
		if !fn(n) {
			continue
		}
		walkNodes(n.Block, fn)
	}
}
