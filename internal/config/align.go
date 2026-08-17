package config

import "fmt"

// alinhar casa a arvore semantica vinda do crossplane com os tokens do
// arquivo, anexando offsets de byte a cada no.
//
// O casamento e por sequencia: o crossplane preserva a ordem do documento, e
// com ParseComments ligado ate os comentarios sao nos da arvore. Entao um
// unico percurso simultaneo resolve tudo — nao ha busca nem heuristica.
func alinhar(f *File) error {
	toks, err := Tokenize(f.Source)
	if err != nil {
		return fmt.Errorf("ao tokenizar %s: %w", f.Path, err)
	}

	a := &aligner{file: f.Path, toks: toks}
	if err := a.nos(f.Nodes); err != nil {
		return err
	}
	if a.pos != len(a.toks) {
		return fmt.Errorf("%s: sobraram %d tokens apos alinhar a arvore",
			f.Path, len(a.toks)-a.pos)
	}
	return nil
}

type aligner struct {
	file string
	toks []Token
	pos  int
}

func (a *aligner) nos(nodes []*Node) error {
	for _, n := range nodes {
		if err := a.no(n); err != nil {
			return err
		}
	}
	return nil
}

func (a *aligner) no(n *Node) error {
	if n.IsComment() {
		tok, err := a.consumir(TokenComment)
		if err != nil {
			return err
		}
		n.Line, n.Column = tok.Line, tok.Column
		n.Span = Span{tok.Start, tok.End}
		n.HeadSpan = n.Span
		return nil
	}

	nome, err := a.consumir(TokenWord)
	if err != nil {
		return err
	}
	n.Line, n.Column = nome.Line, nome.Column

	fimDaCabeca := nome.End
	for range n.Args {
		arg, err := a.consumir(TokenWord)
		if err != nil {
			return err
		}
		fimDaCabeca = arg.End
	}
	n.HeadSpan = Span{nome.Start, fimDaCabeca}

	// Olhar o proximo token e mais confiavel que inspecionar n.Block: um
	// bloco vazio e indistinguivel de uma diretiva simples pelo campo Block.
	proximo, err := a.espiar()
	if err != nil {
		return err
	}

	switch proximo.Kind {
	case TokenSemicolon:
		fim, _ := a.consumir(TokenSemicolon)
		n.temBloco = false
		n.Span = Span{nome.Start, fim.End}
		return nil

	case TokenBlockStart:
		if _, err := a.consumir(TokenBlockStart); err != nil {
			return err
		}
		if err := a.nos(n.Block); err != nil {
			return err
		}
		fim, err := a.consumir(TokenBlockEnd)
		if err != nil {
			return err
		}
		n.temBloco = true
		n.Span = Span{nome.Start, fim.End}
		return nil

	default:
		return fmt.Errorf("%s:%d: esperava ';' ou '{' apos %q, encontrei %q",
			a.file, proximo.Line, n.Directive, proximo.Raw)
	}
}

func (a *aligner) espiar() (Token, error) {
	if a.pos >= len(a.toks) {
		return Token{}, fmt.Errorf("%s: fim inesperado da configuracao", a.file)
	}
	return a.toks[a.pos], nil
}

func (a *aligner) consumir(kind TokenKind) (Token, error) {
	tok, err := a.espiar()
	if err != nil {
		return Token{}, err
	}
	if tok.Kind != kind {
		return Token{}, fmt.Errorf("%s:%d:%d: token inesperado %q",
			a.file, tok.Line, tok.Column, tok.Raw)
	}
	a.pos++
	return tok, nil
}
