package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// luaBodyOf feeds src to the scanner and returns the body up to the closing
// brace, plus whether the block closed at all. src starts just after the "{".
func luaBodyOf(src string) (string, bool) {
	s := newLuaScanner()
	var body strings.Builder
	for i := 0; i < len(src); i++ {
		if s.feed(src[i]) {
			return body.String(), true
		}
		body.WriteByte(src[i])
	}
	return body.String(), false
}

// Every case here is a shape that the delimitation crossplane ships gets
// wrong, checked against what OpenResty 1.27.1.2 accepts. The oracle in
// test/bench asserts the same thing end to end against the real binary; this
// test is the same knowledge at the level where it is decided, so a failure
// says which rule broke instead of only that the file was refused.
func TestTheLuaScannerFindsTheEndOfTheBlock(t *testing.T) {
	cases := []struct {
		name string
		src  string
		body string
		why  string
	}{
		{
			name: "plain body",
			src:  ` ngx.say("ok") }`,
			body: ` ngx.say("ok") `,
			why:  "the case that already worked, kept so a regression is visible",
		},
		{
			name: "brace inside a short string",
			src:  ` local s = "a; b { c }" }`,
			body: ` local s = "a; b { c }" `,
			why:  "a brace in a string is not a brace",
		},
		{
			name: "escaped single quote",
			src:  ` local s = 'a\'b' }`,
			body: ` local s = 'a\'b' `,
			why:  "the backslash escapes the quote, so the string does not end there",
		},
		{
			name: "escaped double quote",
			src:  ` local s = "a\"b" }`,
			body: ` local s = "a\"b" `,
			why:  "same rule, other quote",
		},
		{
			name: "escaped backslash before the quote",
			src:  ` local s = "a\\" } `,
			body: ` local s = "a\\" `,
			why:  "the backslash escapes itself, so the quote DOES close and the brace counts",
		},
		{
			name: "unbalanced brace in a long bracket",
			src:  ` local s = [[ } ]] }`,
			body: ` local s = [[ } ]] `,
			why:  "a long bracket is a string, so the brace inside it is text",
		},
		{
			name: "long bracket with a level",
			src:  ` local s = [==[ } ]] ]==] }`,
			body: ` local s = [==[ } ]] ]==] `,
			why:  "only a closer of the same level closes: the inner ]] is text",
		},
		{
			name: "index is not a long bracket",
			src:  ` local v = t[1] if v then end }`,
			body: ` local v = t[1] if v then end `,
			why:  "a single [ followed by anything but [ or = is indexing",
		},
		{
			name: "unbalanced brace in a short comment",
			src:  " -- }\n ngx.say(1) }",
			body: " -- }\n ngx.say(1) ",
			why:  "the divergence that made ngx accept what OpenResty refuses",
		},
		{
			name: "comment that reaches the end of the line only",
			src:  " -- comment\n if x then end }",
			body: " -- comment\n if x then end ",
			why:  "a short comment ends at the newline and code resumes",
		},
		{
			name: "unbalanced brace in a long comment",
			src:  " --[[ } ]] ngx.say(1) }",
			body: " --[[ } ]] ngx.say(1) ",
			why:  "a long comment spans lines and hides braces the same way",
		},
		{
			name: "long comment with a level",
			src:  " --[=[ } ]] ]=] ngx.say(1) }",
			body: " --[=[ } ]] ]=] ngx.say(1) ",
			why:  "levels apply to comments as they do to brackets",
		},
		{
			name: "minus that is not a comment",
			src:  ` local n = a - b - c }`,
			body: ` local n = a - b - c `,
			why:  "one dash is subtraction; only two open a comment",
		},
		{
			name: "nested braces in code",
			src:  ` local t = { a = { b = 1 } } }`,
			body: ` local t = { a = { b = 1 } } `,
			why:  "real nesting still counts",
		},
		{
			name: "unterminated string does not swallow the rest",
			src:  " local s = \"oops\n ngx.say(1) }",
			body: " local s = \"oops\n ngx.say(1) ",
			why: "an unterminated short string is a Lua error, and Lua ends it at " +
				"the newline; staying inside it would swallow every later brace",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, closed := luaBodyOf(c.src)
			require.Truef(t, closed, "the block never closed: %s", c.why)
			require.Equalf(t, c.body, body, "%s", c.why)
		})
	}
}

// A body that never closes has to stay unclosed. Reporting a false end is the
// failure that produces a tree describing a file that does not exist.
func TestTheLuaScannerDoesNotInventAnEnd(t *testing.T) {
	for _, src := range []string{
		` ngx.say("ok") `,           // simply truncated
		` local s = "unclosed }`,    // brace inside a string, at end of input
		` local s = [[ unclosed } `, // brace inside a long bracket
		" -- unclosed }",            // brace inside a comment, no newline after
	} {
		_, closed := luaBodyOf(src)
		require.Falsef(t, closed, "an end was invented for %q", src)
	}
}
