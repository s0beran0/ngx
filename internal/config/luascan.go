package config

// Where a Lua block ends, decided by Lua's own lexical rules.
//
// This is the one piece of knowledge that both readers of a `*_by_lua_block`
// body have to share: our tokenizer, which produces the byte spans, and the
// lexer we register with crossplane, which produces the tree. If the two
// disagree by a single byte, the aligner cannot match the streams and the
// result is a span pointing at somebody else's text.
//
// It exists because the delimitation crossplane ships is not Lua's. Its lexer
// counts braces outside `'` and `"` and knows nothing else, which was measured
// against OpenResty 1.27.1.2 and found wrong in four ways:
//
//	local s = 'a\'b'      the backslash escapes nothing there, so the second
//	                      quote closes the string and the block never ends
//	local s = "a\"b"      same
//	local s = [[ } ]]     a long bracket is not a string to it, so the brace
//	                      inside counts and the block closes early
//	-- }                  a comment is not a comment to it, same effect
//
// The last one is the serious member of the family, and the reason this file
// is not a nicety. `content_by_lua_block { -- }` closes early, ngx ACCEPTS the
// file, and OpenResty REFUSES it: the tree describes a structure the running
// server never had, with nothing in the output saying so. Editing against that
// tree is a cut in the wrong place.
//
// The alternative was to wait for a fix upstream. We do not have to: the Lexer
// interface crossplane exposes is public (crossplane/lex.go:48-53), and
// registering our own is what turns "their bug" into "our rule".
//
// It is reported anyway, with the reproductions above and the offer of a patch:
// nginxinc/nginx-go-crossplane#179. Keeping the workaround and filing the bug
// are not alternatives -- the workaround is what makes ngx correct today, and
// the report is what eventually makes this file unnecessary. If it lands, the
// oracle in test/bench is what will say so.
//
// What is deliberately NOT here: running Lua, or checking that the body is
// valid Lua. `openresty -t` does not do that either -- it only finds where the
// block ends -- so this file answers exactly the question the server asks and
// no more.

// luaState is where in Lua's grammar the scanner currently is. Braces are
// counted in luaCode and nowhere else.
type luaState int

const (
	luaCode luaState = iota
	luaShortString
	luaLongBracket
	luaShortComment
	luaLongComment
)

// luaScanner finds the "}" that closes a Lua block, one byte at a time.
//
// Byte at a time and not by regex or lookahead over a buffer, because the
// crossplane Lexer interface hands over a rune scanner and nothing else
// (crossplane/lex.go:106-131): there is no way to look ahead, so the state
// machine has to be able to decide with what it has already seen.
type luaScanner struct {
	state luaState
	depth int

	// Short strings: which quote opened, and whether the previous byte was a
	// backslash. Lua's escape rules are richer than this, but every one of
	// them starts with a backslash, and "the next byte is literal" is the only
	// consequence that can move a delimiter.
	quote   byte
	escaped bool

	// Long brackets and long comments carry a level: [[ is 0, [=[ is 1,
	// [==[ is 2, and only ]]/]=]/]==] of the SAME level closes them.
	level int

	// Partial matches in progress. A long bracket opener and a `--` are two
	// bytes or more, so recognising them means remembering that the previous
	// byte could still start one.
	openBracketEquals int  // saw "[" then this many "="
	inOpenBracket     bool // saw "[", possibly followed by "="
	closeEquals       int  // saw "]" then this many "="
	inCloseBracket    bool
	sawDash           bool // saw one "-" in code, which may become "--"
	afterDashDash     bool // saw "--", which may become a long comment
}

// newLuaScanner returns a scanner positioned just after the "{" that opens the
// block, which is where both callers are when they start.
func newLuaScanner() *luaScanner {
	return &luaScanner{state: luaCode, depth: 1}
}

// feed consumes one byte and reports whether it was the "}" that closed the
// block. When it returns true the byte is the closing brace and belongs to
// nobody: it is a delimiter, not part of the body.
//
// helpers would scatter a single transition table across the file.
//
//nolint:gocyclo // Lua's lexical states are what they are; splitting this into
func (s *luaScanner) feed(c byte) (closed bool) {
	switch s.state {
	case luaShortString:
		s.feedShortString(c)
		return false

	case luaLongBracket, luaLongComment:
		s.feedLongForm(c)
		return false

	case luaShortComment:
		// A short comment runs to end of line, and nothing inside it counts.
		// This single line is the fix for the divergence that let ngx accept
		// a file OpenResty refuses.
		if c == '\n' {
			s.state = luaCode
		}
		return false
	}

	// luaCode.
	if s.afterDashDash {
		// "--" was just seen: "--[" may still open a long comment.
		s.afterDashDash = false
		if c == '[' {
			s.inOpenBracket, s.openBracketEquals = true, 0
			s.state = luaLongComment
			return false
		}
		s.state = luaShortComment
		if c == '\n' {
			s.state = luaCode
		}
		return false
	}

	if s.inOpenBracket {
		switch {
		case c == '=':
			s.openBracketEquals++
			return false
		case c == '[':
			s.inOpenBracket = false
			s.level = s.openBracketEquals
			s.state = luaLongBracket
			return false
		default:
			// Not a long bracket after all: "[" was indexing, and whatever
			// this byte is has to be judged as ordinary code.
			s.inOpenBracket = false
		}
	}

	if s.sawDash {
		s.sawDash = false
		if c == '-' {
			s.afterDashDash = true
			return false
		}
	}

	switch c {
	case '-':
		s.sawDash = true
	case '[':
		s.inOpenBracket, s.openBracketEquals = true, 0
	case '\'', '"':
		s.state, s.quote, s.escaped = luaShortString, c, false
	case '{':
		s.depth++
	case '}':
		s.depth--
		return s.depth == 0
	}
	return false
}

func (s *luaScanner) feedShortString(c byte) {
	switch {
	case s.escaped:
		// Whatever it is, it is literal. This is the whole of the fix for
		// 'a\'b' and "a\"b".
		s.escaped = false
	case c == '\\':
		s.escaped = true
	case c == s.quote:
		s.state = luaCode
	case c == '\n':
		// An unterminated short string is a Lua syntax error, and Lua closes
		// it at the newline. Staying "inside the string" for the rest of the
		// file would swallow every brace after it, which turns one bad line
		// into a wrong delimiter for the whole block.
		s.state = luaCode
	}
}

// feedLongForm advances inside a long bracket or a long comment, which close
// the same way: "]" then as many "=" as the opener had, then "]".
func (s *luaScanner) feedLongForm(c byte) {
	if s.inCloseBracket {
		switch {
		case c == '=':
			s.closeEquals++
			return
		case c == ']':
			if s.closeEquals == s.level {
				s.inCloseBracket = false
				s.state = luaCode
				return
			}
			// Wrong level. This "]" can still be the start of the right
			// closer, so the count restarts here rather than giving up.
			s.closeEquals = 0
			return
		default:
			s.inCloseBracket = false
		}
	}
	if c == ']' {
		s.inCloseBracket, s.closeEquals = true, 0
	}
}
