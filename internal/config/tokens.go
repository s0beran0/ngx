package config

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// TokenKind classifies a token.
type TokenKind int

const (
	// TokenWord is a directive name or an argument.
	TokenWord TokenKind = iota
	TokenSemicolon
	TokenBlockStart
	TokenBlockEnd
	TokenComment
)

// Token is a lexeme with its exact position. Value is the semantic content
// (no quotes, no leading # of a comment); Raw is the original text.
//
// Start and End are ALWAYS byte offsets: src[Start:End] == Raw, always, no
// exception -- that is what underpins the surgical editing of v0.2. Line and
// Column, on the other hand, count RUNES, not bytes: they exist for human and
// agent reading as a visual position (the same thing a text editor would
// show), and a multibyte character (c, a, e with a diacritic) counts as a
// single column. For exact byte offsets use Start/End; never derive a byte
// offset from Column.
type Token struct {
	Kind   TokenKind
	Value  string
	Raw    string
	Start  int
	End    int
	Line   int
	Column int
	Quoted bool
}

type tokenizer struct {
	src    []byte
	pos    int
	line   int
	col    int
	tokens []Token
}

// Tokenize breaks the source into tokens with byte offsets. It interprets no
// directive at all: it only needs to know where each lexeme begins and ends,
// honoring quotes, escapes, parameter expansion (${...}) and comments --
// matching nginx-go-crossplane's lexer token for token, which is what Task 9
// uses to align against the semantic tree.
func Tokenize(src []byte) ([]Token, error) {
	t := &tokenizer{src: src, line: 1, col: 1}
	for {
		t.pularEspacos()
		if t.pos >= len(t.src) {
			return t.tokens, nil
		}
		if err := t.proximo(); err != nil {
			return nil, err
		}
	}
}

// runeAqui returns the rune beginning at t.pos and its size in bytes, without
// advancing. utf8.DecodeRune already returns (RuneError, 1) for an invalid
// byte or a truncated sequence -- the tokenizer never gets stuck on malformed
// input (the fuzz produces such input on purpose), it just advances 1 byte at
// a time through it, the same way bufio.ScanRunes does in crossplane's lexer.
func (t *tokenizer) runeAqui() (rune, int) {
	if t.pos >= len(t.src) {
		return 0, 0
	}
	return utf8.DecodeRune(t.src[t.pos:])
}

// espacoAqui says whether the rune at t.pos is whitespace in the full unicode
// sense -- \v, \f and NBSP (U+00A0) included, not just the four ascii bytes.
// It is the same set crossplane uses through strings.TrimSpace.
func (t *tokenizer) espacoAqui() bool {
	r, _ := t.runeAqui()
	return unicode.IsSpace(r)
}

// avancar consumes one whole rune (1 or more bytes) starting at t.pos,
// updating position, line and column. Column counts runes; pos stays in bytes.
func (t *tokenizer) avancar() {
	r, tam := t.runeAqui()
	if tam == 0 {
		return
	}
	t.pos += tam
	if r == '\n' {
		t.line++
		t.col = 1
	} else {
		t.col++
	}
}

// consumirParaValor consumes the rune at t.pos and returns the bytes that
// should go into Value: for a valid rune, the source bytes themselves; for an
// invalid UTF-8 byte, the encoding of the replacement rune U+FFFD -- which is
// what bufio.ScanRunes (the scanner crossplane's lexer uses) returns for
// invalid bytes, even though it advances only 1 byte in the input source.
// Without this, an invalid byte in the middle of a word makes our Value
// disagree with crossplane's, breaking the differential comparison of Task 9.
func (t *tokenizer) consumirParaValor() []byte {
	r, tam := t.runeAqui()
	antes := t.pos
	t.avancar()
	if r == utf8.RuneError && tam == 1 {
		return []byte(string(utf8.RuneError))
	}
	return t.src[antes:t.pos]
}

// consumirEscape consumes the backslash at t.pos and, right after it, any run
// of \r -- each one invisible, just like in crossplane -- until it finds the
// real rune that forms the escape pair with that backslash (already with
// invalid bytes replaced by U+FFFD through consumirParaValor). This
// replicates a genuine crossplane behavior: the "escape pending" state
// crosses a stray \r and merges with the NEXT real character, wherever it may
// be -- not with the \r itself. If the source ends before that rune is found
// (there was only the backslash and perhaps a few \r up to the end of the
// file), the pair never forms: everything consumed stays invisible (it never
// goes into Value, but it still advances the position, so it stays inside the
// Raw of whatever token is being built), and ok comes back false.
func (t *tokenizer) consumirEscape() (proximo []byte, ok bool) {
	t.avancar() // the backslash itself, always 1 ascii byte
	for t.pos < len(t.src) && t.src[t.pos] == '\r' {
		t.avancar()
	}
	if t.pos >= len(t.src) {
		return nil, false
	}
	return t.consumirParaValor(), true
}

func (t *tokenizer) pularEspacos() {
	for t.pos < len(t.src) && t.espacoAqui() {
		t.avancar()
	}
}

func (t *tokenizer) proximo() error {
	start, line, col := t.pos, t.line, t.col

	switch c := t.src[t.pos]; {
	case c == ';':
		t.avancar()
		t.emitir(TokenSemicolon, ";", start, line, col, false)
		return nil
	case c == '{':
		t.avancar()
		t.emitir(TokenBlockStart, "{", start, line, col, false)
		return nil
	case c == '}':
		t.avancar()
		t.emitir(TokenBlockEnd, "}", start, line, col, false)
		return nil
	case c == '#':
		t.lerComentario(start, line, col)
		return nil
	case c == '"' || c == '\'':
		return t.lerAspas(c, start, line, col)
	default:
		return t.lerPalavra(start, line, col)
	}
}

// lerComentario consumes a comment up to the end of the line. The CR of a
// CRLF terminator stays out of the token span: it belongs to the whitespace
// that follows, not to the comment. That way v0.2, when rewriting that
// comment, never converts the line break from CRLF to LF -- an off-target
// change the project promises never to make.
func (t *tokenizer) lerComentario(start, line, col int) {
	for t.pos < len(t.src) {
		if t.src[t.pos] == '\n' {
			break
		}
		if t.src[t.pos] == '\r' && t.pos+1 < len(t.src) && t.src[t.pos+1] == '\n' {
			break
		}
		t.avancar()
	}
	t.emitir(TokenComment, string(t.src[start+1:t.pos]), start, line, col, false)
}

// lerAspas consumes a string between single or double quotes. A backslash is
// only dropped when it precedes the active quote (the current delimiter); any
// other escape stays literal in Value, just like in crossplane -- which is
// why msg "a\nb"; yields Value a\nb (a literal backslash and n), not a real
// line break. A stray \r never goes into Value, it stays invisible, just like
// in crossplane -- but it still advances the position, so it stays inside
// Raw. A backslash followed by \r skips the \r (invisible) and forms the
// escape pair with the real rune that comes next, through consumirEscape.
func (t *tokenizer) lerAspas(aspa byte, start, line, col int) error {
	t.avancar() // consumes the opening quote

	var valor []byte
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		switch {
		case c == '\\':
			if prox, ok := t.consumirEscape(); ok {
				if len(prox) == 1 && prox[0] == aspa {
					valor = append(valor, prox...) // the quote only, no backslash
				} else {
					valor = append(valor, '\\')
					valor = append(valor, prox...)
				}
			}
		case c == '\r':
			// A stray CR stays invisible -- it never goes into Value.
			t.avancar()
		case c == aspa:
			t.avancar() // consumes the closing quote
			t.emitir(TokenWord, string(valor), start, line, col, true)
			return nil
		default:
			valor = append(valor, t.consumirParaValor()...)
		}
	}
	return &ErroDeAspa{Aspa: string(aspa), Linha: line}
}

// ErroDeAspa is the source ending inside an open quote. It is a type, and not
// an fmt.Errorf, because this is one of the enumerated divergences against
// crossplane: their lexer closes the quote implicitly at end of file
// (lex.go:325-327, "if token.Len() > 0 { emit(tokenStartLine, lexState ==
// inQuote, nil) }") and emits no token at all when the content is empty (same
// guard), so that a dangling quote yields an "ok" config for them. nginx
// refuses it; we refuse it too, and the fuzz has to recognize this refusal by
// its class, not by a substring of the message.
type ErroDeAspa struct {
	Aspa  string
	Linha int
}

func (e *ErroDeAspa) Error() string {
	return fmt.Sprintf("quote %q opened on line %d was never closed", e.Aspa, e.Linha)
}

// lerPalavra consumes an unquoted word: a directive name or an argument. It
// treats ${...} (parameter expansion, common in Docker/envsubst templates,
// njs, rewrite and set) as part of the same word -- without that handling,
// phantom "{" and "}" show up in the middle of the word and throw Task 9 out
// of alignment against crossplane's tree. A stray \r stays invisible in the
// middle of the word, just like in crossplane: it does not end the word and
// never goes into Value -- only a real \n ends it. A backslash skips any \r
// that comes right after it and forms the escape pair with the next real
// rune, through consumirEscape; if the source ends before that, the backslash
// (and the \r) vanish leaving no content, exactly like in crossplane.
func (t *tokenizer) lerPalavra(start, line, col int) error {
	var valor []byte
	for t.pos < len(t.src) {
		if len(valor) > 0 && valor[len(valor)-1] == '$' && t.src[t.pos] == '{' {
			antes := t.pos
			t.avancar() // consumes the '{' that opens the expansion
			valor = append(valor, t.src[antes:t.pos]...)
			t.lerVar(&valor)
			continue
		}

		c := t.src[t.pos]
		if c == '\\' {
			if prox, ok := t.consumirEscape(); ok {
				valor = append(valor, '\\')
				valor = append(valor, prox...)
			}
			continue
		}
		if c == '\r' {
			// mirrors lerComentario: the CR of a CRLF terminator stays out
			// of the span, it belongs to the whitespace that follows, not to
			// the word. Only a stray CR (with no \n after it) is invisible
			// and consumed here.
			if t.pos+1 < len(t.src) && t.src[t.pos+1] == '\n' {
				break
			}
			t.avancar()
			continue
		}
		if t.espacoAqui() || c == ';' || c == '{' || c == '}' {
			break
		}

		valor = append(valor, t.consumirParaValor()...)
	}
	if len(valor) == 0 {
		// the only thing consumed was a backslash (and maybe some \r)
		// swallowed without ever finding a pair: there is no content at all,
		// and crossplane produces no token for it either.
		return nil
	}
	t.emitir(TokenWord, string(valor), start, line, col, false)
	return nil
}

// lerVar consumes the body of a parameter expansion (${...}) after the
// opening '{' has already been folded into the word by lerPalavra. It mirrors
// the inVar state of crossplane's lexer, byte by byte: the reading stops
// (back to normal word mode) at the first '}' or the first unescaped
// whitespace, and both are still part of the same word -- odd behavior
// (crossplane itself documents it as a bug, "does not terminate on token
// boundary"), but it is what it does, and this tokenizer has to match it
// token for token, not fix it. A backslash escaping anything (except '}')
// never counts as the whitespace that ends the expansion, only a backslash
// escaping '}' ends it, just like in crossplane. A stray \r stays invisible,
// as in lerAspas; a backslash skips any \r before forming the escape pair,
// through consumirEscape.
func (t *tokenizer) lerVar(valor *[]byte) {
	for t.pos < len(t.src) {
		c := t.src[t.pos]

		if c == '\\' {
			if prox, ok := t.consumirEscape(); ok {
				*valor = append(*valor, '\\')
				*valor = append(*valor, prox...)
				if len(prox) == 1 && prox[0] == '}' {
					return
				}
			}
			continue
		}
		if c == '\r' {
			// same handling as in lerPalavra: the CR of a CRLF stays out of
			// the span, it does not go into the expansion.
			if t.pos+1 < len(t.src) && t.src[t.pos+1] == '\n' {
				return
			}
			t.avancar()
			continue
		}

		espaco := t.espacoAqui()
		*valor = append(*valor, t.consumirParaValor()...)
		if espaco || (*valor)[len(*valor)-1] == '}' {
			return
		}
	}
}

func (t *tokenizer) emitir(kind TokenKind, valor string, start, line, col int, quoted bool) {
	t.tokens = append(t.tokens, Token{
		Kind:   kind,
		Value:  valor,
		Raw:    string(t.src[start:t.pos]),
		Start:  start,
		End:    t.pos,
		Line:   line,
		Column: col,
		Quoted: quoted,
	})
}
