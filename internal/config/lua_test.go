package config_test

import (
	"testing"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

// parseOne parses src and returns the file, demanding acceptance. The whole
// point of R8 is that these configurations are ACCEPTED, so a failure here is
// the defect itself.
func parseOne(t *testing.T, src string) *config.File {
	t.Helper()
	tree, err := config.Parse(config.ParseOptions{Path: writeConf(t, src)})
	require.NoError(t, err, "ngx refused a configuration nginx with lua-nginx-module accepts")
	require.Len(t, tree.Files, 1)
	return tree.Files[0]
}

// The C1 defect of the coverage report: a `content_by_lua_block` whose body
// contains an `if` used to come out as NGX-0003, complaining about an "if"
// expression that is Lua code. nginx with lua-nginx-module takes this file,
// so ngx was refusing a VALID configuration -- the worst class of defect this
// tool can have.
func TestLuaBlockWithIfIsAccepted(t *testing.T) {
	src := "content_by_lua_block {\n" +
		"    if t.x > 0 then ngx.say(\"oi; tchau\") end\n" +
		"}\n"

	file := parseOne(t, src)

	require.Len(t, file.Nodes, 1)
	n := file.Nodes[0]
	require.Equal(t, "content_by_lua_block", n.Directive)
	require.False(t, n.HasBlock(), "the body is an argument, not a block of directives")
	require.Equal(t, []string{"\n    if t.x > 0 then ngx.say(\"oi; tchau\") end\n"}, n.Args)
}

// The other half of the same defect: the ';' and the braces inside the Lua
// body are Lua, and must not be read as configuration.
func TestLuaBodyKeepsSemicolonsAndBraces(t *testing.T) {
	for _, tc := range []struct{ name, src, body string }{
		{"semicolon", "content_by_lua_block { ngx.say(\"a; b\") }\n", ` ngx.say("a; b") `},
		{"nested braces", "content_by_lua_block { local t = { a = 1 } }\n", " local t = { a = 1 } "},
		{"brace inside string", "content_by_lua_block { s = \"}\" }\n", ` s = "}" `},
		{"empty body", "content_by_lua_block {}\n", ""},
		{"if inside a server", "http { server { rewrite_by_lua_block { if x then end } } }\n", " if x then end "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := parseOne(t, tc.src)
			var found *config.Node
			walk(file.Nodes, func(n *config.Node) {
				if config.IsLuaBlockDirective(n.Directive) {
					found = n
				}
			})
			require.NotNil(t, found)
			require.Equal(t, []string{tc.body}, found.Args)
		})
	}
}

// THE property this task turns on. Registering the Lua lexer makes crossplane
// emit ONE token for a body our tokenizer used to split into many; if the two
// streams desynchronise, every span after the block is wrong -- and wrong in
// silence, which is worse than the refusal it replaces. Here the spans of
// everything that comes AFTER the block are compared against the text they
// are supposed to point at.
func TestSpansAfterLuaBlockStayCorrect(t *testing.T) {
	src := "server {\n" +
		"    content_by_lua_block {\n" +
		"        if t.x > 0 then ngx.say(\"oi; tchau\") end\n" +
		"    }\n" +
		"    listen 8080;\n" +
		"    server_name a.example;\n" +
		"}\n" +
		"# after\n"

	file := parseOne(t, src)
	require.Len(t, file.Nodes, 2)

	server := file.Nodes[0]
	require.Equal(t, "server", server.Directive)
	body := server.Block
	require.Len(t, body, 3)

	// The two directives after the Lua block: span, head span, per-argument
	// span, line and column, all against the text.
	listen := body[1]
	require.Equal(t, "listen", listen.Directive)
	require.Equal(t, "listen 8080;", text(file, listen.Span))
	require.Equal(t, "listen 8080", text(file, listen.HeadSpan))
	require.Len(t, listen.ArgSpans, 1)
	require.Equal(t, "8080", text(file, listen.ArgSpans[0]))
	require.Equal(t, 5, listen.Line)
	require.Equal(t, 5, listen.Column)

	name := body[2]
	require.Equal(t, "server_name", name.Directive)
	require.Equal(t, "server_name a.example;", text(file, name.Span))
	require.Equal(t, "a.example", text(file, name.ArgSpans[0]))
	require.Equal(t, 6, name.Line)

	// And the comment after the whole block, at the root level.
	comment := file.Nodes[1]
	require.Equal(t, "#", comment.Directive)
	require.Equal(t, "# after", text(file, comment.Span))
	require.Equal(t, 8, comment.Line)
}

// The span of the body itself, which the aligner records as the span of the
// argument. It covers the WHOLE lexeme, braces included -- the same rule as
// the quotes of a quoted argument (see Node.ArgSpans) --, and the directive's
// span ends at the closing brace even though the terminator crossplane emits
// for it does not exist in the file.
func TestLuaBodyArgSpanCoversTheBraces(t *testing.T) {
	src := "content_by_lua_block { ngx.say(\"oi\") }\nlisten 80;\n"

	file := parseOne(t, src)
	n := file.Nodes[0]

	require.Len(t, n.ArgSpans, 1)
	require.Equal(t, "{ ngx.say(\"oi\") }", text(file, n.ArgSpans[0]))
	require.Equal(t, "content_by_lua_block { ngx.say(\"oi\") }", text(file, n.Span))
	require.Equal(t, n.Span, n.HeadSpan, "there is no terminator in the file to separate the two")

	// The body, minus its braces, is exactly the argument crossplane read.
	require.Equal(t, n.Args[0], text(file, config.Span{Start: n.ArgSpans[0].Start + 1, End: n.ArgSpans[0].End - 1}))
}

// set_by_lua_block is the one Lua directive that takes an argument before the
// body, and crossplane reads it with a lexer of its own (lua.go:68-90): a run
// of non-space characters, with no notion of quote. `"$a"` is therefore its
// value, quotation marks and all -- and the span has to reproduce that,
// otherwise a v0.2 rewrite would replace the wrong bytes.
func TestSetByLuaBlockArguments(t *testing.T) {
	src := "set_by_lua_block $sum { return 1 + 1 }\nlisten 80;\n"

	file := parseOne(t, src)
	n := file.Nodes[0]

	require.Equal(t, []string{"$sum", " return 1 + 1 "}, n.Args)
	require.Len(t, n.ArgSpans, 2)
	require.Equal(t, "$sum", text(file, n.ArgSpans[0]))
	require.Equal(t, "{ return 1 + 1 }", text(file, n.ArgSpans[1]))

	require.Equal(t, "listen 80;", text(file, file.Nodes[1].Span))
}

// Two Lua blocks in a row, which is where an off-by-one in the implied
// terminator would show up first.
func TestTwoLuaBlocksInARow(t *testing.T) {
	src := "access_by_lua_block { a() }\nlog_by_lua_block { b() }\nlisten 80;\n"

	file := parseOne(t, src)
	require.Len(t, file.Nodes, 3)
	require.Equal(t, "access_by_lua_block { a() }", text(file, file.Nodes[0].Span))
	require.Equal(t, "log_by_lua_block { b() }", text(file, file.Nodes[1].Span))
	require.Equal(t, "listen 80;", text(file, file.Nodes[2].Span))
}

// The name of a Lua directive in ARGUMENT position is an argument, not the
// start of a Lua block: crossplane only takes the Lua path in directive
// position (lex.go:188). Reading it otherwise would refuse a perfectly
// ordinary `server_name`.
func TestLuaDirectiveNameAsArgument(t *testing.T) {
	file := parseOne(t, "server_name content_by_lua_block;\n")

	require.Len(t, file.Nodes, 1)
	require.Equal(t, "server_name", file.Nodes[0].Directive)
	require.Equal(t, []string{"content_by_lua_block"}, file.Nodes[0].Args)
}

// --- enumerated divergences ------------------------------------------------

// A *_by_lua_block with no body: crossplane's hook (lex.go:186-206) reads one
// character past the name and re-processes it AFTER the block it already
// consumed, so its token stream comes out of document order -- and for some
// of these inputs it still returns a payload with Status "ok" (see the test
// right below). Tokens carrying byte spans cannot describe that stream, and
// there is nothing to describe anyway: nginx refuses all of them
// -- `content_by_lua_blockx` is an unknown directive, and a directive with no
// body has the wrong number of arguments -- so the refusal is the right
// answer; what matters is that it comes out TYPED, matching the entry in
// knownDivergence.
func TestDivergenceLuaBlockWithoutBody(t *testing.T) {
	for _, src := range []string{
		"content_by_lua_blockx { y }\n",
		"content_by_lua_blockfoo { x }\n",
		"content_by_lua_block ;\n",
		"content_by_lua_block\\\n{ x }\n",
	} {
		t.Run(src, func(t *testing.T) {
			pe := refusal(t, src)
			require.Equal(t, config.RefusalInvalidLuaBlock, pe.Class)
			require.True(t, config.IsLuaBlockDirective(pe.Token),
				"the token of the refusal has to be the name of a Lua directive, was %q", pe.Token)
			require.Equal(t, 1, pe.Line)
		})
	}
}

// And this one is ACCEPTED by crossplane, which is what makes the refusal a
// divergence and not an ordinary parse error: the Lua lexer gives up on the
// missing "{", the main lexer resumes on the character it had read ahead, and
// what comes out is `content_by_lua_block` with the arguments ["", "fo"] and
// a block holding an `x` -- a tree assembled out of a stream whose tokens no
// longer follow the text.
func TestDivergenceLuaBlockWithoutBodyIsAcceptedByCrossplane(t *testing.T) {
	acceptedByCrossplane(t, writeConf(t, "content_by_lua_blockfoo { x }\n"))
}

// The file ending inside a Lua block. Crossplane refuses it too ("premature
// end of file"), so there is no divergence here -- what is checked is that
// the refusal is typed and points at the directive, instead of coming out as
// the dependency's raw string.
func TestUnterminatedLuaBlockIsTypedRefusal(t *testing.T) {
	for _, src := range []string{
		"content_by_lua_block { unterminated\n",
		"content_by_lua_block   ",
		"set_by_lua_block $a\n",
	} {
		t.Run(src, func(t *testing.T) {
			pe := refusal(t, src)
			require.Equal(t, config.RefusalInvalidLuaBlock, pe.Class)
		})
	}
}

// This used to be a limitation, and the test that recorded it asserted a
// REFUSAL: crossplane's Lua lexer treats a backslash as an ordinary character
// (lua.go:148-155), so `'a\'b'` closed the string at the escaped quote and the
// block never found its brace. nginx accepts that body, and we did not.
//
// It is now accepted, because the delimitation is ours (luascan.go) instead of
// the dependency's. The test is kept, inverted, for the reason the old one
// existed: the boundary is a decision on record. If a future change hands the
// Lua body back to crossplane's lexer, this goes red rather than quietly
// reintroducing a refusal of valid OpenResty configuration.
func TestALuaBodyWithAnEscapedQuoteIsAccepted(t *testing.T) {
	src := "http { server { location / {\n" +
		"log_by_lua_block { ngx.log(ngx.ERR, 'a\\'b') }\n" +
		"access_log off;\n} } }\n"

	tree, err := config.Parse(config.ParseOptions{Path: writeConf(t, src)})
	require.NoError(t, err, "a body nginx accepts must not be refused")

	var lua *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "log_by_lua_block" {
			lua = n
		}
		return true
	})
	require.NotNil(t, lua)
	require.Equal(t, []string{` ngx.log(ngx.ERR, 'a\'b') `}, lua.Args,
		"the escaped quote is inside the string, so the body runs to the real brace")

	// And the directive AFTER the block is still a directive: an escaped quote
	// that had moved the delimiter would have swallowed this one.
	var after bool
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "access_log" {
			after = true
		}
		return true
	})
	require.True(t, after, "the directive after the block was swallowed")
}

// --- helpers ---------------------------------------------------------------

func text(f *config.File, s config.Span) string { return string(f.Source[s.Start:s.End]) }

func walk(nodes []*config.Node, fn func(*config.Node)) {
	for _, n := range nodes {
		fn(n)
		walk(n.Block, fn)
	}
}
