package config

import (
	"fmt"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
)

// LuaLexOptions is the registration parse.go hands to crossplane, and the
// reason this file exists at all. Without it a `content_by_lua_block` whose
// body contains an `if` is refused with NGX-0003 -- a valid OpenResty
// configuration rejected, which is the worst thing this tool can do.
//
// It is exported for one reason: the tests that use crossplane as an ORACLE
// have to register the same extension, or they would be comparing ngx against
// a parser reading a different language. Sharing the value is what keeps the
// oracle from drifting away from production.
func LuaLexOptions() crossplane.LexOptions {
	// OUR lexer, not crossplane.Lua's. The one it ships counts braces outside
	// `'` and `"` and knows nothing else, which makes it disagree with
	// OpenResty on escaped quotes, long brackets and comments -- and in the
	// comment case it accepts a file the server refuses. See luascan.go for
	// the measurements and lualexer.go for the replacement.
	//
	// The registration point is the public one (crossplane/lex.go:85-90), so
	// this is the extension mechanism working as intended rather than a way
	// around it.
	var lexer luaLexer
	return crossplane.LexOptions{
		Lexers: []crossplane.RegisterLexer{
			crossplane.LexWithLexer(lexer, lexer.directiveNames()...),
		},
	}
}

// Support for the *_by_lua_block directives of lua-nginx-module (OpenResty).
//
// The body of one of those directives is Lua code, not nginx configuration:
// it has its own `if`, its own braces, its own strings. Read as nginx, a body
// like `if t.x > 0 then ngx.say("oi; tchau") end` becomes an "if" directive
// with a malformed expression, and ngx refuses a file nginx accepts -- the
// worst class of defect this tool can have.
//
// Crossplane already solves it: Lua.RegisterLexer (crossplane/lua.go:47-49)
// installs an external lexer that turns the whole body into ONE opaque token.
// parse.go registers it, and this file is the other half of the fix: our
// tokenizer has to produce the same stream, token for token, because the
// aligner matches the two by sequence. A crossplane emitting one token where
// we emit twenty desynchronises every span after the block -- silently, which
// would be worse than the refusal it replaces.
//
// Everything here is therefore a replication of crossplane/lua.go and of the
// external-lexer hook in crossplane/lex.go:186-206, not a design of our own.
// Where the replication is impossible -- crossplane emits, in those cases,
// tokens whose text sits BEFORE the block it already consumed, so no
// increasing sequence of byte spans can describe them -- the tokenizer
// refuses with LuaBlockError instead of guessing.

// luaBlockDirectives replicates, name by name, Lua.directiveNames()
// (crossplane/lua.go:25-44). A name outside this list is an ordinary
// directive, block and all.
var luaBlockDirectives = map[string]bool{
	"init_by_lua_block":              true,
	"init_worker_by_lua_block":       true,
	"exit_worker_by_lua_block":       true,
	"set_by_lua_block":               true,
	"content_by_lua_block":           true,
	"server_rewrite_by_lua_block":    true,
	"rewrite_by_lua_block":           true,
	"access_by_lua_block":            true,
	"header_filter_by_lua_block":     true,
	"body_filter_by_lua_block":       true,
	"log_by_lua_block":               true,
	"balancer_by_lua_block":          true,
	"ssl_client_hello_by_lua_block":  true,
	"ssl_certificate_by_lua_block":   true,
	"ssl_session_fetch_by_lua_block": true,
	"ssl_session_store_by_lua_block": true,
}

// setByLuaBlock is the one directive that takes an argument BEFORE the body
// (crossplane/lua.go:9 and 68-90).
const setByLuaBlock = "set_by_lua_block"

// IsLuaBlockDirective reports whether the name is one of the *_by_lua_block
// directives whose body is Lua code read as a single opaque argument. It is
// exported for the tests that check the shape of those spans; nothing in the
// production path branches on it, because the tokenizer already resolved the
// question when it produced the tokens.
func IsLuaBlockDirective(name string) bool { return luaBlockDirectives[name] }

// LuaBlockError is a *_by_lua_block that does not have a body ngx can point
// at. It is a type, and not an fmt.Errorf, because it is an enumerated
// divergence against crossplane, like QuoteError: for some of these inputs
// crossplane still returns a payload, built out of tokens that come back OUT
// OF DOCUMENT ORDER.
//
// That out-of-order stream is the whole reason for the refusal, and it is
// worth being concrete about it. crossplane's hook reads one character past
// the directive name before handing control to the Lua lexer, and
// re-processes that character AFTER the block has been consumed
// (crossplane/lex.go:194-204 with readNext still true): for
// `content_by_lua_blockx { y }` its token stream is
// [content_by_lua_block, " y ", ";", x] -- the "x" is text that sits before
// the block, emitted after it. Our tokens carry byte spans and are matched
// against that stream in order, so there is no honest span to give the "x".
// nginx, for its part, refuses all of these: `content_by_lua_blockx` is an
// unknown directive, and a `*_by_lua_block` with no `{` is a directive with
// the wrong number of arguments.
type LuaBlockError struct {
	Directive string
	Line      int
	What      string
}

func (e *LuaBlockError) Error() string {
	return fmt.Sprintf("directive %q on line %d: %s", e.Directive, e.Line, e.What)
}

// luaTriggers replicates the external-lexer check of crossplane/lex.go:186-206:
// the lexer compares the token buffer it has ACCUMULATED SO FAR, on every
// character, against the registered names -- so the trigger fires at the
// first character after the name, whatever that character is, and the buffer
// must be a whole registered name for it to fire.
//
// It also replicates the two conditions the check depends on. The first is
// nextTokenIsDirective, which is not "is at the beginning of a line": it is
// true at the start of the file, after `;`, `{` and `}`, after a QUOTED token
// (crossplane/lex.go:318-322 never clears it) and after a line break that did
// not close a word -- and false right after any word that ended in
// whitespace, which is why `foo bar\ncontent_by_lua_block { x }` does NOT
// take the Lua path while the same text with a blank line between them does.
// The second is the end-of-line update of lex.go:164-167, which runs on the
// current lookahead BEFORE the check, and is what makes
// `foo content_by_lua_block\n{ x }` take it.
func (t *tokenizer) luaTriggers(value []byte) bool {
	if len(value) == 0 || !luaBlockDirectives[string(value)] {
		return false
	}
	if t.lookaheadIsEOL() {
		t.nextIsDirective = true
	}
	return t.nextIsDirective
}

// lookaheadIsEOL reports whether the next unit crossplane's lexer would scan
// ends in "\n": bare \r are skipped before anything else (lex.go:173-175) and
// a backslash is merged with the character after it (lex.go:177-184), so
// `\` + `\n` counts as an end of line too.
func (t *tokenizer) lookaheadIsEOL() bool {
	i := t.pos
	for i < len(t.src) && t.src[i] == '\r' {
		i++
	}
	if i < len(t.src) && t.src[i] == '\\' {
		i++
		for i < len(t.src) && t.src[i] == '\r' {
			i++
		}
	}
	return i < len(t.src) && t.src[i] == '\n'
}

// readLuaBlock emits the directive name and then the body, replicating
// Lua.Lex (crossplane/lua.go:55-176). The caller has already accumulated the
// name; quote is the delimiter of the name when it came quoted, 0 otherwise.
//
// The stream that comes out of it is exactly crossplane's: the name, the body
// as a single quoted token, and a `;` that does not exist in the file
// (crossplane/lua.go:143 emits it "for an end to the Lua string based on the
// nginx behavior"). Ours carries a zero-width span, which is the only honest
// answer for a terminator that occupies no byte -- and it is what lets the
// aligner close the directive with no special case at all.
func (t *tokenizer) readLuaBlock(name string, start, line, col int, quote byte) error {
	if quote != 0 {
		// The lookahead here is the closing quote, which crossplane swallows
		// (lex.go:201-204) -- the name token keeps it inside its Raw, exactly
		// like any other quoted token.
		t.skipCRs()
		if t.pos >= len(t.src) || t.src[t.pos] != quote {
			return &LuaBlockError{Directive: name, Line: line, What: `the name of a *_by_lua_block directive must be followed by whitespace and "{"`}
		}
		t.advance()
		t.emit(TokenWord, name, start, line, col, true)
	} else {
		t.emit(TokenWord, name, start, line, col, false)
		t.skipCRs()
		// The character crossplane read as lookahead is dropped from the
		// stream and re-processed AFTER the block (lex.go:194-204). When it
		// is whitespace that is invisible; when it is anything else the
		// stream comes back out of order -- see LuaBlockError.
		if t.pos >= len(t.src) || !t.spaceHere() {
			return &LuaBlockError{Directive: name, Line: line, What: `the name of a *_by_lua_block directive must be followed by whitespace and "{"`}
		}
		t.advance()
	}

	// The Lua block is a statement of its own: whatever comes after it starts
	// in directive position, as it does for crossplane, which never clears
	// the flag along this path.
	t.nextIsDirective = true

	if name == setByLuaBlock {
		if err := t.readLuaFirstArg(name); err != nil {
			return err
		}
	}
	return t.readLuaBody(name)
}

// readLuaFirstArg replicates the special handling of set_by_lua_block
// (crossplane/lua.go:68-90): the argument is a run of non-space characters,
// read character by character with no notion of quote, escape or brace, and
// emitted UNQUOTED. `set_by_lua_block "$a" { }` therefore has `"$a"`, with
// its quotation marks, as the value of the first argument -- which is why the
// token is marked Verbatim.
func (t *tokenizer) readLuaFirstArg(name string) error {
	for t.pos < len(t.src) && t.spaceHere() {
		t.advance()
	}
	start, line, col := t.pos, t.line, t.col
	var value []byte
	for t.pos < len(t.src) && !t.spaceHere() {
		value = append(value, t.consumeIntoValue()...)
	}
	if len(value) == 0 || t.pos >= len(t.src) {
		// crossplane returns from the goroutine without emitting anything
		// else the moment the scanner runs dry (lua.go:71-73 and 82-84).
		return &LuaBlockError{Directive: name, Line: line, What: "the configuration ends inside the directive"}
	}
	t.emitVerbatim(TokenWord, string(value), start, line, col, false)
	t.advance() // the whitespace that ended the argument, consumed by lua.go:75
	return nil
}

// readLuaBody skips whitespace up to the "{" that opens the block and then
// takes everything up to the matching "}" as one token.
//
// Where that "}" is comes from luaScanner (luascan.go), which applies Lua's
// own lexical rules: braces are counted in code and not inside a short string,
// a long bracket, or either kind of comment. It used to replicate crossplane's
// simpler rule instead, because the dependency decided where the block ended
// and disagreeing would have desynchronised the two token streams. It no
// longer decides: LuaLexOptions registers OUR lexer, so the same luaScanner
// answers for the tree and for these spans, and neither one is wrong.
func (t *tokenizer) readLuaBody(name string) error {
	for t.pos < len(t.src) && t.spaceHere() {
		t.advance()
	}
	if t.pos >= len(t.src) {
		return &LuaBlockError{Directive: name, Line: t.line, What: "the configuration ends inside the directive"}
	}
	if t.src[t.pos] != '{' {
		return &LuaBlockError{Directive: name, Line: t.line, What: `the body of a *_by_lua_block directive must start with "{"`}
	}

	start, line, col := t.pos, t.line, t.col
	t.advance() // the "{" that opens the block: delimiter, out of the value
	scan := newLuaScanner()

	var value []byte
	for t.pos < len(t.src) {
		// One byte to the scanner, one RUNE to the value. That is not a
		// mismatch: every byte the scanner branches on is ASCII, and no
		// continuation byte of a multi-byte rune is, so the lead byte of such a
		// rune reaches it as an ordinary character -- which is what it is.
		closed := scan.feed(t.src[t.pos])
		switch {
		case closed:
			{
				t.advance() // the "}" that closes the block, also a delimiter
				t.emitVerbatim(TokenWord, string(value), start, line, col, true)
				// The implied terminator. It has no bytes of its own, so its
				// span is empty and sits right after the "}": Start == End is
				// what says "this token is not in the file", and any other
				// choice would hand v0.2 a range that holds someone else's
				// bytes.
				t.emit(TokenSemicolon, ";", t.pos, t.line, t.col, false)
				return nil
			}

		default:
			value = append(value, t.consumeIntoValue()...)
		}
	}
	return &LuaBlockError{Directive: name, Line: line, What: "the configuration ends inside the directive"}
}

// skipCRs consumes a run of \r. Crossplane's lexer skips them before doing
// anything else with the character (lex.go:173-175), so they never take part
// in the decision that follows.
func (t *tokenizer) skipCRs() {
	for t.pos < len(t.src) && t.src[t.pos] == '\r' {
		t.advance()
	}
}
