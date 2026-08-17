package config

import "fmt"

// alinhar casa a arvore semantica vinda do crossplane com os tokens do
// arquivo, anexando offsets de byte a cada no.
//
// O casamento e por sequencia, mas "o crossplane preserva a ordem do
// documento" e falso para um caso: um comentario encontrado no meio dos
// argumentos de uma diretiva (crossplane/parse.go:286-290, commentsInArgs)
// nao entra em Args e nao fica onde apareceu no texto -- ele e anexado como
// no "#" irmao DEPOIS da diretiva inteira, e depois do bloco dela se houver
// (parse.go:435-445). O aligner drena esses tokens de comentario do meio dos
// argumentos e os casa, em ordem, com esses nos "#" que aparecem depois --
// ver drenarComentarios e o parametro pendentes.
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
	// pendentes e a fila de tokens de comentario drenados do meio dos
	// argumentos de algum no deste MESMO nivel (irmaos), na ordem em que
	// apareceram no texto. E local a esta chamada de nos() -- e nao um campo
	// do aligner -- porque cada nivel de bloco tem sua propria sequencia de
	// nos "#" apos as diretivas; se fosse compartilhada entre niveis, um
	// comentario dos argumentos de um no com bloco furaria a ordem quando o
	// bloco tivesse, por sua vez, diretivas com comentario nos argumentos.
	var pendentes []Token
	for _, n := range nodes {
		if err := a.no(n, &pendentes); err != nil {
			return err
		}
	}
	return nil
}

func (a *aligner) no(n *Node, pendentes *[]Token) error {
	if n.IsComment() {
		if len(*pendentes) > 0 {
			tok := (*pendentes)[0]
			*pendentes = (*pendentes)[1:]
			n.Line, n.Column = tok.Line, tok.Column
			n.Span = Span{tok.Start, tok.End}
			n.HeadSpan = n.Span
			return nil
		}
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
	a.drenarComentarios(pendentes)

	if n.Directive == "if" {
		// prepareIfArgs (crossplane/util.go:71-86) remove de Args os tokens
		// "(" e ")" quando eles vem isolados (com espaco em volta), entao
		// len(n.Args) nao conta os tokens-palavra reais entre o nome e o
		// terminador -- ver defeito 2 da Task 9. Consome por posicao do
		// terminador, nao por contagem de Args.
		for {
			proximo, err := a.espiar()
			if err != nil {
				return err
			}
			if proximo.Kind == TokenSemicolon || proximo.Kind == TokenBlockStart {
				break
			}
			arg, err := a.consumir(TokenWord)
			if err != nil {
				return err
			}
			fimDaCabeca = arg.End
			a.drenarComentarios(pendentes)
		}
	} else {
		for range n.Args {
			arg, err := a.consumir(TokenWord)
			if err != nil {
				return err
			}
			fimDaCabeca = arg.End
			a.drenarComentarios(pendentes)
		}
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

// drenarComentarios consome, da posicao atual, quantos TokenComment
// estiverem em sequencia, guardando-os em pendentes na ordem em que
// apareceram. Esses tokens nao entram em Args (crossplane/parse.go:286-290)
// e reaparecem como nos "#" irmaos depois que a diretiva atual (e seu
// bloco, se houver) termina -- e ali que no() os consome de volta da fila,
// em vez de tentar ler um TokenComment novo da posicao atual do stream, que
// nesse ponto ja avancou para alem deles.
func (a *aligner) drenarComentarios(pendentes *[]Token) {
	for a.pos < len(a.toks) && a.toks[a.pos].Kind == TokenComment {
		*pendentes = append(*pendentes, a.toks[a.pos])
		a.pos++
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
