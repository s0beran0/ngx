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

	// Verbatim marks a token whose Value is the literal text of the span --
	// delimiters excluded, invalid UTF-8 replaced by U+FFFD, and nothing
	// else interpreted. It is what the Lua lexer of lua-nginx-module blocks
	// produces (see lua.go): inside a *_by_lua_block body a backslash is a
	// backslash and a quote is a quote, because the content is Lua code and
	// not nginx configuration. Without the mark there is no way to tell that
	// token apart from an ordinary word, whose Value has escapes resolved --
	// and the two disagree for the same Raw.
	Verbatim bool
}

type tokenizer struct {
	src    []byte
	pos    int
	line   int
	col    int
	tokens []Token

	// nextIsDirective mirrors crossplane's nextTokenIsDirective
	// (lex.go:146), the flag that decides whether a word in this position is
	// read as a directive NAME -- which is what allows a *_by_lua_block body
	// to be lexed as Lua. See luaTriggers in lua.go for what turns it on and
	// off, which is not what the name suggests.
	nextIsDirective bool
}

// Tokenize breaks the source into tokens with byte offsets. It interprets no
// directive at all: it only needs to know where each lexeme begins and ends,
// honoring quotes, escapes, parameter expansion (${...}) and comments --
// matching nginx-go-crossplane's lexer token for token, which is what Task 9
// uses to align against the semantic tree.
func Tokenize(src []byte) ([]Token, error) {
	t := &tokenizer{src: src, line: 1, col: 1, nextIsDirective: true}
	for {
		t.skipSpaces()
		if t.pos >= len(t.src) {
			return t.tokens, nil
		}
		if err := t.next(); err != nil {
			return nil, err
		}
	}
}

// runeHere returns the rune beginning at t.pos and its size in bytes, without
// advancing. utf8.DecodeRune already returns (RuneError, 1) for an invalid
// byte or a truncated sequence -- the tokenizer never gets stuck on malformed
// input (the fuzz produces such input on purpose), it just advances 1 byte at
// a time through it, the same way bufio.ScanRunes does in crossplane's lexer.
func (t *tokenizer) runeHere() (rune, int) {
	if t.pos >= len(t.src) {
		return 0, 0
	}
	return utf8.DecodeRune(t.src[t.pos:])
}

// spaceHere says whether the rune at t.pos is whitespace in the full unicode
// sense -- \v, \f and NBSP (U+00A0) included, not just the four ascii bytes.
// It is the same set crossplane uses through strings.TrimSpace.
func (t *tokenizer) spaceHere() bool {
	r, _ := t.runeHere()
	return unicode.IsSpace(r)
}

// advance consumes one whole rune (1 or more bytes) starting at t.pos,
// updating position, line and column. Column counts runes; pos stays in bytes.
//
// A line break also puts the lexer back in directive position, mirroring
// crossplane/lex.go:164-167, which does it for EVERY character it scans. The
// flag is cleared again by whoever closes a word on that same character (see
// consumeWordTerminator), exactly as lex.go:230-233 does right after.
func (t *tokenizer) advance() {
	r, size := t.runeHere()
	if size == 0 {
		return
	}
	t.pos += size
	if r == '\n' {
		t.line++
		t.col = 1
		t.nextIsDirective = true
	} else {
		t.col++
	}
}

// consumeIntoValue consumes the rune at t.pos and returns the bytes that
// should go into Value: for a valid rune, the source bytes themselves; for an
// invalid UTF-8 byte, the encoding of the replacement rune U+FFFD -- which is
// what bufio.ScanRunes (the scanner crossplane's lexer uses) returns for
// invalid bytes, even though it advances only 1 byte in the input source.
// Without this, an invalid byte in the middle of a word makes our Value
// disagree with crossplane's, breaking the differential comparison of Task 9.
func (t *tokenizer) consumeIntoValue() []byte {
	r, size := t.runeHere()
	from := t.pos
	t.advance()
	if r == utf8.RuneError && size == 1 {
		return []byte(string(utf8.RuneError))
	}
	return t.src[from:t.pos]
}

// consumeEscape consumes the backslash at t.pos and, right after it, any run
// of \r -- each one invisible, just like in crossplane -- until it finds the
// real rune that forms the escape pair with that backslash (already with
// invalid bytes replaced by U+FFFD through consumeIntoValue). This
// replicates a genuine crossplane behavior: the "escape pending" state
// crosses a stray \r and merges with the NEXT real character, wherever it may
// be -- not with the \r itself. If the source ends before that rune is found
// (there was only the backslash and perhaps a few \r up to the end of the
// file), the pair never forms: everything consumed stays invisible (it never
// goes into Value, but it still advances the position, so it stays inside the
// Raw of whatever token is being built), and ok comes back false.
func (t *tokenizer) consumeEscape() (next []byte, ok bool) {
	t.advance() // the backslash itself, always 1 ascii byte
	for t.pos < len(t.src) && t.src[t.pos] == '\r' {
		t.advance()
	}
	if t.pos >= len(t.src) {
		return nil, false
	}
	return t.consumeIntoValue(), true
}

func (t *tokenizer) skipSpaces() {
	for t.pos < len(t.src) && t.spaceHere() {
		t.advance()
	}
}

func (t *tokenizer) next() error {
	start, line, col := t.pos, t.line, t.col

	// ';', '{' and '}' put the lexer back in directive position and '#' takes
	// it out of it, mirroring crossplane/lex.go:296 and :223. What that flag
	// decides is whether the next word can be a *_by_lua_block name -- see
	// luaTriggers in lua.go.
	switch c := t.src[t.pos]; {
	case c == ';':
		t.advance()
		t.emit(TokenSemicolon, ";", start, line, col, false)
		t.nextIsDirective = true
		return nil
	case c == '{':
		t.advance()
		t.emit(TokenBlockStart, "{", start, line, col, false)
		t.nextIsDirective = true
		return nil
	case c == '}':
		t.advance()
		t.emit(TokenBlockEnd, "}", start, line, col, false)
		t.nextIsDirective = true
		return nil
	case c == '#':
		t.nextIsDirective = false
		t.readComment(start, line, col)
		return nil
	case c == '"' || c == '\'':
		return t.readQuoted(c, start, line, col)
	default:
		return t.readWord(start, line, col)
	}
}

// readComment consumes a comment up to the end of the line. The CR of a
// CRLF terminator stays out of the token span: it belongs to the whitespace
// that follows, not to the comment. That way v0.2, when rewriting that
// comment, never converts the line break from CRLF to LF -- an off-target
// change the project promises never to make.
func (t *tokenizer) readComment(start, line, col int) {
	for t.pos < len(t.src) {
		if t.src[t.pos] == '\n' {
			break
		}
		if t.src[t.pos] == '\r' && t.pos+1 < len(t.src) && t.src[t.pos+1] == '\n' {
			break
		}
		t.advance()
	}
	t.emit(TokenComment, string(t.src[start+1:t.pos]), start, line, col, false)
}

// readQuoted consumes a string between single or double quotes. A backslash is
// only dropped when it precedes the active quote (the current delimiter); any
// other escape stays literal in Value, just like in crossplane -- which is
// why msg "a\nb"; yields Value a\nb (a literal backslash and n), not a real
// line break. A stray \r never goes into Value, it stays invisible, just like
// in crossplane -- but it still advances the position, so it stays inside
// Raw. A backslash followed by \r skips the \r (invisible) and forms the
// escape pair with the real rune that comes next, through consumeEscape.
func (t *tokenizer) readQuoted(quote byte, start, line, col int) error {
	t.advance() // consumes the opening quote

	var value []byte
	for t.pos < len(t.src) {
		// A quoted directive name also reaches the Lua lexer: crossplane
		// checks the accumulated buffer in every state, the quoted one
		// included (lex.go:186-206, with the inQuote special case at
		// :190-204). See readLuaBlock.
		if t.luaTriggers(value) {
			return t.readLuaBlock(string(value), start, line, col, quote)
		}
		c := t.src[t.pos]
		switch {
		case c == '\\':
			if next, ok := t.consumeEscape(); ok {
				if len(next) == 1 && next[0] == quote {
					value = append(value, next...) // the quote only, no backslash
				} else {
					value = append(value, '\\')
					value = append(value, next...)
				}
			}
		case c == '\r':
			// A stray CR stays invisible -- it never goes into Value.
			t.advance()
		case c == quote:
			t.advance() // consumes the closing quote
			t.emit(TokenWord, string(value), start, line, col, true)
			return nil
		default:
			value = append(value, t.consumeIntoValue()...)
		}
	}
	return &QuoteError{Quote: string(quote), Line: line}
}

// QuoteError is the source ending inside an open quote. It is a type, and not
// an fmt.Errorf, because this is one of the enumerated divergences against
// crossplane: their lexer closes the quote implicitly at end of file
// (lex.go:325-327, "if token.Len() > 0 { emit(tokenStartLine, lexState ==
// inQuote, nil) }") and emits no token at all when the content is empty (same
// guard), so that a dangling quote yields an "ok" config for them. nginx
// refuses it; we refuse it too, and the fuzz has to recognize this refusal by
// its class, not by a substring of the message.
type QuoteError struct {
	Quote string
	Line  int
}

func (e *QuoteError) Error() string {
	return fmt.Sprintf("quote %q opened on line %d was never closed", e.Quote, e.Line)
}

// readWord consumes an unquoted word: a directive name or an argument. It
// treats ${...} (parameter expansion, common in Docker/envsubst templates,
// njs, rewrite and set) as part of the same word -- without that handling,
// phantom "{" and "}" show up in the middle of the word and throw Task 9 out
// of alignment against crossplane's tree. A stray \r stays invisible in the
// middle of the word, just like in crossplane: it does not end the word and
// never goes into Value -- only a real \n ends it. A backslash skips any \r
// that comes right after it and forms the escape pair with the next real
// rune, through consumeEscape; if the source ends before that, the backslash
// (and the \r) vanish leaving no content, exactly like in crossplane.
func (t *tokenizer) readWord(start, line, col int) error {
	var value []byte
	endedAtSpace := false
	for t.pos < len(t.src) {
		// The name of a *_by_lua_block directive ends the word here, before
		// the character that follows it is even looked at: from this point
		// on the body is Lua code, not nginx configuration. See lua.go.
		if t.luaTriggers(value) {
			return t.readLuaBlock(string(value), start, line, col, 0)
		}

		if len(value) > 0 && value[len(value)-1] == '$' && t.src[t.pos] == '{' {
			from := t.pos
			t.advance() // consumes the '{' that opens the expansion
			value = append(value, t.src[from:t.pos]...)
			t.readVar(&value)
			continue
		}

		c := t.src[t.pos]
		if c == '\\' {
			if next, ok := t.consumeEscape(); ok {
				value = append(value, '\\')
				value = append(value, next...)
			}
			continue
		}
		if c == '\r' {
			// mirrors readComment: the CR of a CRLF terminator stays out
			// of the span, it belongs to the whitespace that follows, not to
			// the word. Only a stray CR (with no \n after it) is invisible
			// and consumed here.
			if t.pos+1 < len(t.src) && t.src[t.pos+1] == '\n' {
				endedAtSpace = true
				break
			}
			t.advance()
			continue
		}
		if t.spaceHere() {
			endedAtSpace = true
			break
		}
		if c == ';' || c == '{' || c == '}' {
			break
		}

		value = append(value, t.consumeIntoValue()...)
	}
	if len(value) == 0 {
		// the only thing consumed was a backslash (and maybe some \r)
		// swallowed without ever finding a pair: there is no content at all,
		// and crossplane produces no token for it either.
		return nil
	}
	t.emit(TokenWord, string(value), start, line, col, false)
	if endedAtSpace {
		t.consumeWordTerminator()
	}
	return nil
}

// consumeWordTerminator swallows the whitespace that closed the word and
// takes the lexer OUT of directive position. Both halves come from
// crossplane/lex.go:230-233: the character had already been read as lookahead
// before the token was emitted, and nextTokenIsDirective is cleared right
// after -- even when that same character is a line break that had just set it
// (lex.go:164-167). Consuming it here is what keeps the two in that order,
// and it is why `foo bar\ncontent_by_lua_block { x }` does not take the Lua
// path while `foo bar\n\ncontent_by_lua_block { x }` does. Bare \r are
// invisible to that lexer (lex.go:173-175) and are skipped along with it.
func (t *tokenizer) consumeWordTerminator() {
	t.skipCRs()
	if t.pos < len(t.src) {
		t.advance()
	}
	t.nextIsDirective = false
}

// readVar consumes the body of a parameter expansion (${...}) after the
// opening '{' has already been folded into the word by readWord. It mirrors
// the inVar state of crossplane's lexer, byte by byte: the reading stops
// (back to normal word mode) at the first '}' or the first unescaped
// whitespace, and both are still part of the same word -- odd behavior
// (crossplane itself documents it as a bug, "does not terminate on token
// boundary"), but it is what it does, and this tokenizer has to match it
// token for token, not fix it. A backslash escaping anything (except '}')
// never counts as the whitespace that ends the expansion, only a backslash
// escaping '}' ends it, just like in crossplane. A stray \r stays invisible,
// as in readQuoted; a backslash skips any \r before forming the escape pair,
// through consumeEscape.
func (t *tokenizer) readVar(value *[]byte) {
	for t.pos < len(t.src) {
		c := t.src[t.pos]

		if c == '\\' {
			if next, ok := t.consumeEscape(); ok {
				*value = append(*value, '\\')
				*value = append(*value, next...)
				if len(next) == 1 && next[0] == '}' {
					return
				}
			}
			continue
		}
		if c == '\r' {
			// Every \r is invisible here, the one before a \n included --
			// and that exception is NOT the same as readWord's. There the
			// \n ends the word, so leaving the \r out of the span changes
			// nothing but the span. Inside an expansion the \n does not end
			// anything: crossplane skips the \r (lex.go:173-175) and then
			// writes the \n into the token, which goes on growing. Returning
			// here instead used to end the word one token early --
			// "${\r\nx" came out as two tokens against crossplane's one,
			// which is a desync of the aligner, not a cosmetic difference.
			// Found by FuzzTokenizeSpans (corpus fb069b4c398a72be).
			t.advance()
			continue
		}

		space := t.spaceHere()
		*value = append(*value, t.consumeIntoValue()...)
		if space || (*value)[len(*value)-1] == '}' {
			return
		}
	}
}

func (t *tokenizer) emit(kind TokenKind, value string, start, line, col int, quoted bool) {
	t.tokens = append(t.tokens, Token{
		Kind:   kind,
		Value:  value,
		Raw:    string(t.src[start:t.pos]),
		Start:  start,
		End:    t.pos,
		Line:   line,
		Column: col,
		Quoted: quoted,
	})
}

// emitVerbatim emits a token whose Value is the literal text of the span --
// see the Verbatim field of Token.
func (t *tokenizer) emitVerbatim(kind TokenKind, value string, start, line, col int, quoted bool) {
	t.emit(kind, value, start, line, col, quoted)
	t.tokens[len(t.tokens)-1].Verbatim = true
}
