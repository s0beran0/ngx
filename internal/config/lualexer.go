package config

import (
	"strings"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
)

// luaLexer is the lexer ngx registers with crossplane for the
// `*_by_lua_block` directives, in place of the one crossplane ships.
//
// It exists so that the delimitation of a Lua body is decided in ONE place,
// luaScanner, for both readers: the tree crossplane builds and the byte spans
// our tokenizer produces. Before this, the dependency decided, our tokenizer
// replicated its decision to stay aligned, and both were wrong together in the
// four ways luascan.go lists -- measured against OpenResty, not reasoned about.
//
// Replacing it is legitimate rather than a hack: crossplane exposes Lexer as a
// public interface for exactly this, and LexWithLexer as the way to register
// one (crossplane/lex.go:48-53 and 85-90). Nothing here reaches into the
// package's internals.
//
// The token stream it emits is deliberately identical in SHAPE to
// crossplane/lua.go's -- name, then the body as one quoted token, then a ";"
// that is not in the file -- because the parser downstream is theirs and
// expects that shape. What changes is only where the body ends.
type luaLexer struct{}

// directiveNames is the same list crossplane's Lua extension registers
// (crossplane/lua.go:25-44), taken from our own table so that the set of
// directives treated as Lua cannot drift between the lexer and
// IsLuaBlockDirective.
func (luaLexer) directiveNames() []string {
	names := make([]string, 0, len(luaBlockDirectives))
	for name := range luaBlockDirectives {
		names = append(names, name)
	}
	return names
}

// Lex consumes the Lua block and emits its tokens.
//
// The scanner hands over one rune at a time and there is no way to look ahead
// or push back (crossplane/lex.go:106-131), which is why luaScanner is a state
// machine fed byte by byte rather than something that matches over a buffer.
func (l luaLexer) Lex(s *crossplane.SubScanner, matchedToken string) <-chan crossplane.NgxToken {
	tokenCh := make(chan crossplane.NgxToken)

	go func() {
		defer close(tokenCh)

		fail := func(what string) {
			line := s.Line()
			tokenCh <- crossplane.NgxToken{
				Error: &crossplane.ParseError{What: what, Line: &line},
			}
		}

		// set_by_lua_block is the only one of these directives that carries an
		// argument before the body, and crossplane reads it as a plain run of
		// non-space characters with no notion of quoting (crossplane/lua.go:68-90).
		// That behaviour is kept: it is what nginx itself does with the variable
		// name, and our tokenizer already replicates it in readLuaFirstArg.
		if matchedToken == setByLuaBlock {
			var arg strings.Builder
			for {
				if !s.Scan() {
					fail("the configuration ends inside the directive")
					return
				}
				next := s.Text()
				if !isSpaceToken(next) {
					arg.WriteString(next)
					continue
				}
				if arg.Len() > 0 {
					break
				}
			}
			tokenCh <- crossplane.NgxToken{Value: arg.String(), Line: s.Line()}
		}

		// Everything up to the "{" that opens the block has to be whitespace.
		// Anything else means the directive name was being used as something
		// other than a Lua block -- `server_name content_by_lua_block;` is the
		// case crossplane calls out -- and the token is handed back untouched so
		// the ordinary lexer deals with it.
		for {
			if !s.Scan() {
				fail("the configuration ends inside the directive")
				return
			}
			next := s.Text()
			if isSpaceToken(next) {
				continue
			}
			if next != "{" {
				tokenCh <- crossplane.NgxToken{Value: next, Line: s.Line()}
				return
			}
			break
		}

		// The body, delimited by Lua's rules.
		scan := newLuaScanner()
		var body strings.Builder
		for {
			if !s.Scan() {
				fail("the configuration ends inside the directive")
				return
			}
			if err := s.Err(); err != nil {
				fail(err.Error())
				return
			}

			next := s.Text()
			if next == "" {
				continue
			}
			if scan.feed(next[0]) {
				// The closing brace is a delimiter and not part of the body.
				//
				// IsQuoted is what tells the parser downstream that this token
				// is one argument and not a stream of directives; the ";" after
				// it is the terminator crossplane's own lexer invents for the
				// same reason (crossplane/lua.go:143), because nginx treats the
				// block as ending the statement.
				tokenCh <- crossplane.NgxToken{Value: body.String(), Line: s.Line(), IsQuoted: true}
				tokenCh <- crossplane.NgxToken{Value: ";", Line: s.Line()}
				return
			}
			body.WriteString(next)
		}
	}()

	return tokenCh
}

// isSpaceToken answers for the one-rune strings the SubScanner produces.
func isSpaceToken(s string) bool {
	if len(s) != 1 {
		return false
	}
	switch s[0] {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}
