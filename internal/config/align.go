package config

import (
	"errors"
	"fmt"
)

// alinhar matches the semantic tree coming from crossplane against the tokens
// of the file, attaching byte offsets to every node.
//
// The matching is by sequence, but "crossplane preserves document order" is
// false in one case: a comment found in the middle of a directive's arguments
// (crossplane/parse.go:286-290, commentsInArgs) does not go into Args and
// does not stay where it appeared in the text -- it is attached as a sibling
// "#" node AFTER the whole directive, and after its block if there is one
// (parse.go:435-445). The aligner drains those comment tokens from the middle
// of the arguments and matches them, in order, against those "#" nodes that
// come later -- see drenarComentarios and the pendentes parameter.
//
// Inside a map-like body this reattachment does NOT happen: the statement is
// appended at parse.go:318 and the loop does continue at parse.go:319, before
// the commentsInArgs loop of parse.go:436, so the comments from the middle of
// the arguments are discarded by crossplane. There the queue has to be
// discarded as well -- see ehCorpoMapLike.
func alinhar(f *File) error {
	toks, err := Tokenize(f.Source)
	if err != nil {
		var aspa *ErroDeAspa
		if errors.As(err, &aspa) {
			return ParseErrors{{
				File:    f.Path,
				Line:    aspa.Linha,
				Message: aspa.Error(),
				Classe:  RecusaAspaNaoFechada,
				Token:   aspa.Aspa,
			}}
		}
		return fmt.Errorf("while tokenizing %s: %w", f.Path, err)
	}

	a := &aligner{file: f.Path, toks: toks}
	if err := a.nos(f.Nodes, false); err != nil {
		return err
	}
	if a.pos != len(a.toks) {
		sobrou := a.toks[a.pos]
		return ParseErrors{{
			File:    f.Path,
			Line:    sobrou.Line,
			Message: fmt.Sprintf("%d tokens left over after aligning the tree", len(a.toks)-a.pos),
			Classe:  RecusaTokensSobrando,
			Token:   sobrou.Raw,
		}}
	}
	return nil
}

type aligner struct {
	file string
	toks []Token
	pos  int
}

// corposMapLike replicates, name by name, crossplane's table of the same name
// (analyze_map.go:20-46). The body of such a block is not analyzed as
// directives: parse.go:304-321 appends the statement and moves on.
var corposMapLike = map[string]bool{
	"charset_map":   true,
	"geo":           true,
	"map":           true,
	"match":         true,
	"types":         true,
	"split_clients": true,
	"geoip2":        true,
	"otel_exporter": true,
}

func ehCorpoMapLike(diretiva string) bool { return corposMapLike[diretiva] }

func (a *aligner) nos(nodes []*Node, corpoMapLike bool) error {
	// pendentes is the queue of comment tokens drained from the middle of
	// the arguments of some node at this SAME level (siblings), in the order
	// they appeared in the text. It is local to this call of nos() -- and not
	// a field of the aligner -- because each block level has its own sequence
	// of "#" nodes after the directives; were it shared between levels, a
	// comment from the arguments of a node with a block would break the order
	// whenever that block in turn had directives with comments in their
	// arguments.
	//
	// fila == nil marks a map-like body: there crossplane discards the
	// comments from the middle of the arguments instead of reattaching them,
	// so there is no "#" node at all to claim the queue. Queueing them anyway
	// would make the next standalone comment at the same level match the
	// wrong token -- and would refuse a valid config.
	var pendentes []Token
	fila := &pendentes
	if corpoMapLike {
		fila = nil
	}
	for _, n := range nodes {
		if err := a.no(n, fila); err != nil {
			return err
		}
	}
	return nil
}

func (a *aligner) no(n *Node, pendentes *[]Token) error {
	if n.IsComment() {
		if pendentes != nil && len(*pendentes) > 0 {
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

	nome, err := a.consumirNomeDeDiretiva()
	if err != nil {
		return err
	}
	n.Line, n.Column = nome.Line, nome.Column

	fimDaCabeca := nome.End
	var comentariosDaCabeca []Token
	a.drenarComentarios(pendentes, &comentariosDaCabeca)

	if n.Directive == "if" {
		// prepareIfArgs (crossplane/util.go:71-86) removes the "(" and ")"
		// tokens from Args when they come isolated (with whitespace around
		// them), so len(n.Args) does not count the real word tokens between
		// the name and the terminator -- see defect 2 of Task 9. Consume by
		// the position of the terminator, not by the count of Args.
		for {
			proximo, err := a.espiar()
			if err != nil {
				return err
			}
			// A TokenBlockEnd also ends the collection: crossplane's
			// argument loop stops at "}" (parse.go:285), so "x { if (a) }"
			// is an "if" with no terminator, not an "if" with "}" as an
			// argument. Stopping here is what makes the refusal come out
			// classified as RecusaTerminadorAusente -- a known divergence --
			// instead of an unexpected token, which is the aligner's bug class.
			if proximo.Kind == TokenSemicolon || proximo.Kind == TokenBlockStart ||
				proximo.Kind == TokenBlockEnd {
				break
			}
			arg, err := a.consumir(TokenWord)
			if err != nil {
				return err
			}
			fimDaCabeca = arg.End
			a.drenarComentarios(pendentes, &comentariosDaCabeca)
		}
	} else {
		for range n.Args {
			arg, err := a.consumir(TokenWord)
			if err != nil {
				return err
			}
			fimDaCabeca = arg.End
			a.drenarComentarios(pendentes, &comentariosDaCabeca)
		}
	}
	n.HeadSpan = Span{nome.Start, fimDaCabeca}
	n.HeadComments = comentariosDentro(comentariosDaCabeca, n.HeadSpan)

	// Looking at the next token is more reliable than inspecting n.Block: an
	// empty block is indistinguishable from a plain directive by the Block
	// field alone.
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
		if err := a.nos(n.Block, ehCorpoMapLike(n.Directive)); err != nil {
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
		return ParseErrors{{
			File:    a.file,
			Line:    proximo.Line,
			Message: fmt.Sprintf("expected ';' or '{' after %q, found %q", n.Directive, proximo.Raw),
			Classe:  RecusaTerminadorAusente,
			Token:   proximo.Raw,
		}}
	}
}

// comentariosDentro filters the drained comments, keeping only the ones that
// fall inside the head. The last drain of a directive may pick up comments
// that sit AFTER the last argument ("a b # c\n;"): those fall outside
// HeadSpan, and recording them would be lying about the range.
func comentariosDentro(toks []Token, head Span) []Span {
	var dentro []Span
	for _, t := range toks {
		if t.Start >= head.Start && t.End <= head.End {
			dentro = append(dentro, Span{t.Start, t.End})
		}
	}
	return dentro
}

// drenarComentarios consumes, from the current position, as many TokenComment
// as sit in a row, keeping them in pendentes in the order they appeared.
// Those tokens do not go into Args (crossplane/parse.go:286-290) and reappear
// as sibling "#" nodes once the current directive (and its block, if any)
// ends -- that is where no() takes them back off the queue, instead of trying
// to read a fresh TokenComment from the current stream position, which by
// then has already moved past them.
//
// pendentes == nil is the map-like body: crossplane discards those comments
// (parse.go:319 does continue before parse.go:436), so here they are
// discarded too. Discarded from the queue, not from the file: they stay
// recorded in coletados, which becomes Node.HeadComments.
func (a *aligner) drenarComentarios(pendentes *[]Token, coletados *[]Token) {
	for a.pos < len(a.toks) && a.toks[a.pos].Kind == TokenComment {
		tok := a.toks[a.pos]
		if pendentes != nil {
			*pendentes = append(*pendentes, tok)
		}
		*coletados = append(*coletados, tok)
		a.pos++
	}
}

func (a *aligner) espiar() (Token, error) {
	if a.pos >= len(a.toks) {
		return Token{}, ParseErrors{{
			File:    a.file,
			Message: "unexpected end of configuration",
			Classe:  RecusaFimInesperado,
		}}
	}
	return a.toks[a.pos], nil
}

func (a *aligner) consumir(kind TokenKind) (Token, error) {
	tok, err := a.espiar()
	if err != nil {
		return Token{}, err
	}
	if tok.Kind != kind {
		return Token{}, a.tokenInesperado(tok, RecusaTokenInesperado)
	}
	a.pos++
	return tok, nil
}

// consumirNomeDeDiretiva reads the token that opens a statement. It is kept
// apart from consumir only for the class of the refusal: crossplane accepts
// ANY token value as a directive name (parse.go:256-261 builds the Directive
// out of t.Value without checking that it is a word; only "}" at
// parse.go:237 and comments at parse.go:264 are handled separately), so "{}"
// becomes for it a directive named "{". We refuse it, as nginx does, and the
// class exists so that the fuzz recognizes this divergence by the exact shape
// of the token.
func (a *aligner) consumirNomeDeDiretiva() (Token, error) {
	tok, err := a.espiar()
	if err != nil {
		return Token{}, err
	}
	if tok.Kind != TokenWord {
		return Token{}, a.tokenInesperado(tok, RecusaTokenNoLugarDeDiretiva)
	}
	a.pos++
	return tok, nil
}

func (a *aligner) tokenInesperado(tok Token, classe ClasseRecusa) error {
	return ParseErrors{{
		File:    a.file,
		Line:    tok.Line,
		Message: fmt.Sprintf("column %d: unexpected token %q", tok.Column, tok.Raw),
		Classe:  classe,
		Token:   tok.Raw,
	}}
}
