package config

import (
	"errors"
	"fmt"
)

// align matches the semantic tree coming from crossplane against the tokens
// of the file, attaching byte offsets to every node.
//
// The matching is by sequence, but "crossplane preserves document order" is
// false in one case: a comment found in the middle of a directive's arguments
// (crossplane/parse.go:286-290, commentsInArgs) does not go into Args and
// does not stay where it appeared in the text -- it is attached as a sibling
// "#" node AFTER the whole directive, and after its block if there is one
// (parse.go:435-445). The aligner drains those comment tokens from the middle
// of the arguments and matches them, in order, against those "#" nodes that
// come later -- see drainComments and the pending parameter.
//
// Inside a map-like body this reattachment does NOT happen: the statement is
// appended at parse.go:318 and the loop does continue at parse.go:319, before
// the commentsInArgs loop of parse.go:436, so the comments from the middle of
// the arguments are discarded by crossplane. There the queue has to be
// discarded as well -- see isMapLikeBody.
func align(f *File) error {
	toks, err := Tokenize(f.Source)
	if err != nil {
		var quote *QuoteError
		if errors.As(err, &quote) {
			return ParseErrors{{
				File:    f.Path,
				Line:    quote.Line,
				Message: quote.Error(),
				Class:   RefusalUnclosedQuote,
				Token:   quote.Quote,
			}}
		}
		return fmt.Errorf("while tokenizing %s: %w", f.Path, err)
	}

	a := &aligner{file: f.Path, toks: toks}
	if err := a.alignNodes(f.Nodes, false); err != nil {
		return err
	}
	if a.pos != len(a.toks) {
		leftover := a.toks[a.pos]
		return ParseErrors{{
			File:    f.Path,
			Line:    leftover.Line,
			Message: fmt.Sprintf("%d tokens left over after aligning the tree", len(a.toks)-a.pos),
			Class:   RefusalLeftoverTokens,
			Token:   leftover.Raw,
		}}
	}
	return nil
}

type aligner struct {
	file string
	toks []Token
	pos  int
}

// mapLikeBodies replicates, name by name, crossplane's table of the same name
// (analyze_map.go:20-46). The body of such a block is not analyzed as
// directives: parse.go:304-321 appends the statement and moves on.
var mapLikeBodies = map[string]bool{
	"charset_map":   true,
	"geo":           true,
	"map":           true,
	"match":         true,
	"types":         true,
	"split_clients": true,
	"geoip2":        true,
	"otel_exporter": true,
}

func isMapLikeBody(directive string) bool { return mapLikeBodies[directive] }

func (a *aligner) alignNodes(nodes []*Node, mapLikeBody bool) error {
	// pending is the queue of comment tokens drained from the middle of
	// the arguments of some node at this SAME level (siblings), in the order
	// they appeared in the text. It is local to this call of alignNodes() -- and not
	// a field of the aligner -- because each block level has its own sequence
	// of "#" nodes after the directives; were it shared between levels, a
	// comment from the arguments of a node with a block would break the order
	// whenever that block in turn had directives with comments in their
	// arguments.
	//
	// queue == nil marks a map-like body: there crossplane discards the
	// comments from the middle of the arguments instead of reattaching them,
	// so there is no "#" node at all to claim the queue. Queueing them anyway
	// would make the next standalone comment at the same level match the
	// wrong token -- and would refuse a valid config.
	var pending []Token
	queue := &pending
	if mapLikeBody {
		queue = nil
	}
	for _, n := range nodes {
		if err := a.alignNode(n, queue); err != nil {
			return err
		}
	}
	return nil
}

func (a *aligner) alignNode(n *Node, pending *[]Token) error {
	if n.IsComment() {
		if pending != nil && len(*pending) > 0 {
			tok := (*pending)[0]
			*pending = (*pending)[1:]
			n.Line, n.Column = tok.Line, tok.Column
			n.Span = Span{tok.Start, tok.End}
			n.HeadSpan = n.Span
			n.ArgSpans = []Span{}
			return nil
		}
		tok, err := a.consume(TokenComment)
		if err != nil {
			return err
		}
		n.Line, n.Column = tok.Line, tok.Column
		n.Span = Span{tok.Start, tok.End}
		n.HeadSpan = n.Span
		n.ArgSpans = []Span{}
		return nil
	}

	name, err := a.consumeDirectiveName()
	if err != nil {
		return err
	}
	n.Line, n.Column = name.Line, name.Column

	headEnd := name.End
	var headComments []Token
	a.drainComments(pending, &headComments)

	if n.Directive == "if" {
		// prepareIfArgs (crossplane/util.go:71-86) removes the "(" and ")"
		// tokens from Args when they come isolated (with whitespace around
		// them), so len(n.Args) does not count the real word tokens between
		// the name and the terminator -- see defect 2 of Task 9. Consume by
		// the position of the terminator, not by the count of Args.
		//
		// n.ArgSpans is left nil here, and that is the whole answer for "if":
		// the same rewrite that breaks the count also breaks the
		// correspondence when the count happens to match. In "if ($a = b)"
		// the first lexeme is "($a" and Args[0] is "$a"; recording the lexeme
		// would give v0.2 a range that includes the parenthesis, and
		// recording a trimmed range would be us guessing where crossplane cut.
		// Nil says "unavailable" -- see the field's documentation in node.go.
		for {
			next, err := a.peek()
			if err != nil {
				return err
			}
			// A TokenBlockEnd also ends the collection: crossplane's
			// argument loop stops at "}" (parse.go:285), so "x { if (a) }"
			// is an "if" with no terminator, not an "if" with "}" as an
			// argument. Stopping here is what makes the refusal come out
			// classified as RefusalMissingTerminator -- a known divergence --
			// instead of an unexpected token, which is the aligner's bug class.
			if next.Kind == TokenSemicolon || next.Kind == TokenBlockStart ||
				next.Kind == TokenBlockEnd {
				break
			}
			arg, err := a.consume(TokenWord)
			if err != nil {
				return err
			}
			headEnd = arg.End
			a.drainComments(pending, &headComments)
		}
	} else {
		// Built unconditionally out of the tokens actually consumed, with no
		// check that each one reproduces its argument. That check belongs to
		// the differential test, not to the code: a loop that only recorded
		// the span when it matched the argument would make its own test
		// tautological -- it could never observe a mismatch, because it would
		// have refused to record it.
		spans := make([]Span, 0, len(n.Args))
		for range n.Args {
			arg, err := a.consume(TokenWord)
			if err != nil {
				return err
			}
			headEnd = arg.End
			spans = append(spans, Span{arg.Start, arg.End})
			a.drainComments(pending, &headComments)
		}
		n.ArgSpans = spans
	}
	n.HeadSpan = Span{name.Start, headEnd}
	n.HeadComments = commentsInside(headComments, n.HeadSpan)

	// Looking at the next token is more reliable than inspecting n.Block: an
	// empty block is indistinguishable from a plain directive by the Block
	// field alone.
	next, err := a.peek()
	if err != nil {
		return err
	}

	switch next.Kind {
	case TokenSemicolon:
		end, _ := a.consume(TokenSemicolon)
		n.hasBlock = false
		n.Span = Span{name.Start, end.End}
		return nil

	case TokenBlockStart:
		if _, err := a.consume(TokenBlockStart); err != nil {
			return err
		}
		if err := a.alignNodes(n.Block, isMapLikeBody(n.Directive)); err != nil {
			return err
		}
		end, err := a.consume(TokenBlockEnd)
		if err != nil {
			return err
		}
		n.hasBlock = true
		n.Span = Span{name.Start, end.End}
		return nil

	default:
		return ParseErrors{{
			File:    a.file,
			Line:    next.Line,
			Message: fmt.Sprintf("expected ';' or '{' after %q, found %q", n.Directive, next.Raw),
			Class:   RefusalMissingTerminator,
			Token:   next.Raw,
		}}
	}
}

// commentsInside filters the drained comments, keeping only the ones that
// fall inside the head. The last drain of a directive may pick up comments
// that sit AFTER the last argument ("a b # c\n;"): those fall outside
// HeadSpan, and recording them would be lying about the range.
func commentsInside(toks []Token, head Span) []Span {
	var inside []Span
	for _, t := range toks {
		if t.Start >= head.Start && t.End <= head.End {
			inside = append(inside, Span{t.Start, t.End})
		}
	}
	return inside
}

// drainComments consumes, from the current position, as many TokenComment
// as sit in a row, keeping them in pending in the order they appeared.
// Those tokens do not go into Args (crossplane/parse.go:286-290) and reappear
// as sibling "#" nodes once the current directive (and its block, if any)
// ends -- that is where alignNode() takes them back off the queue, instead of trying
// to read a fresh TokenComment from the current stream position, which by
// then has already moved past them.
//
// pending == nil is the map-like body: crossplane discards those comments
// (parse.go:319 does continue before parse.go:436), so here they are
// discarded too. Discarded from the queue, not from the file: they stay
// recorded in collected, which becomes Node.HeadComments.
func (a *aligner) drainComments(pending *[]Token, collected *[]Token) {
	for a.pos < len(a.toks) && a.toks[a.pos].Kind == TokenComment {
		tok := a.toks[a.pos]
		if pending != nil {
			*pending = append(*pending, tok)
		}
		*collected = append(*collected, tok)
		a.pos++
	}
}

func (a *aligner) peek() (Token, error) {
	if a.pos >= len(a.toks) {
		return Token{}, ParseErrors{{
			File:    a.file,
			Message: "unexpected end of configuration",
			Class:   RefusalUnexpectedEnd,
		}}
	}
	return a.toks[a.pos], nil
}

func (a *aligner) consume(kind TokenKind) (Token, error) {
	tok, err := a.peek()
	if err != nil {
		return Token{}, err
	}
	if tok.Kind != kind {
		return Token{}, a.unexpectedToken(tok, RefusalUnexpectedToken)
	}
	a.pos++
	return tok, nil
}

// consumeDirectiveName reads the token that opens a statement. It is kept
// apart from consume only for the class of the refusal: crossplane accepts
// ANY token value as a directive name (parse.go:256-261 builds the Directive
// out of t.Value without checking that it is a word; only "}" at
// parse.go:237 and comments at parse.go:264 are handled separately), so "{}"
// becomes for it a directive named "{". We refuse it, as nginx does, and the
// class exists so that the fuzz recognizes this divergence by the exact shape
// of the token.
func (a *aligner) consumeDirectiveName() (Token, error) {
	tok, err := a.peek()
	if err != nil {
		return Token{}, err
	}
	if tok.Kind != TokenWord {
		return Token{}, a.unexpectedToken(tok, RefusalTokenInsteadOfDirective)
	}
	a.pos++
	return tok, nil
}

func (a *aligner) unexpectedToken(tok Token, class RefusalClass) error {
	return ParseErrors{{
		File:    a.file,
		Line:    tok.Line,
		Message: fmt.Sprintf("column %d: unexpected token %q", tok.Column, tok.Raw),
		Class:   class,
		Token:   tok.Raw,
	}}
}
