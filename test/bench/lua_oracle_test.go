//go:build integration

// Package bench_test validates the Lua bench. It lives outside internal/config
// on purpose: the fixture it uses is an ARTEFACT OF THE BENCH rather than of a
// package -- it only means anything when there is a binary with
// lua-nginx-module to answer for it, and the `make bench-lua-up` that brings
// that binary up lives here.
package bench_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

// The container `make bench-lua-up` brings up.
const luaCT = "ngx-bench-lua"

// fixtureLua is the Lua syntax surface, the counterpart of
// internal/config/testdata/syntax_surface.conf.
const fixtureLua = "testdata/lua_surface.conf"

// requireOracle skips the test when the Lua bench is not up. Skipping is more
// honest than failing: whoever runs `go test -tags integration ./...` without
// docker has no defect in their code.
func requireOracle(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available; bring the bench up to run this")
	}
	if err := exec.Command("docker", "inspect", luaCT).Run(); err != nil {
		t.Skip("the Lua bench is not up: run `make bench-lua-up`")
	}
}

// openrestyTest runs a real `openresty -t` over src, inside the container, and
// returns the combined output and whether the binary accepted it.
//
// One measurement worth recording, because it bounds what this oracle proves:
// `openresty -t` does NOT compile the Lua body. `content_by_lua_block { if end }`
// passes, and so does `{ this is not lua !!! }`. The module only lexes the body
// to find where it ENDS -- which is exactly the question ngx answers too. So
// the oracle covers block delimitation, and nothing beyond it.
func openrestyTest(t *testing.T, src string) (string, bool) {
	t.Helper()

	writeIn := exec.Command("docker", "exec", "-i", luaCT,
		"sh", "-c", "cat > /tmp/oracle.conf")
	writeIn.Stdin = strings.NewReader(src)
	require.NoError(t, writeIn.Run(), "could not place the configuration in the container")

	output, err := exec.Command("docker", "exec", luaCT,
		"openresty", "-t", "-c", "/tmp/oracle.conf").CombinedOutput()
	return string(output), err == nil
}

// ngxParse tries to parse src with ngx and returns the file and the error.
func ngxParse(t *testing.T, src string) (*config.File, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ngx.conf")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))
	tree, err := config.Parse(config.ParseOptions{Path: path})
	if err != nil {
		return nil, err
	}
	require.Len(t, tree.Files, 1)
	return tree.Files[0], nil
}

func readFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixtureLua)
	require.NoError(t, err)
	return string(data)
}

func text(f *config.File, s config.Span) string { return string(f.Source[s.Start:s.End]) }

// The oracle that was missing. Until now, everything the project claimed about
// accepting OpenResty configuration rested on crossplane's lexer and on
// reasoning about Lua syntax -- with no binary to say otherwise. This test is
// the binary saying.
func TestTheLuaSurfaceIsAcceptedByRealOpenResty(t *testing.T) {
	requireOracle(t)

	output, ok := openrestyTest(t, readFixture(t))
	require.Truef(t, ok,
		"real OpenResty refused the fixture, so it has stopped being a "+
			"description of valid configuration:\n%s", output)
	require.Contains(t, output, "syntax is ok")
}

// The other half: ngx reads the SAME fixture and arrives at the same values.
// Without this, the test above would prove only that the fixture is valid, not
// that we agree with whoever validates it.
func TestNgxReadsTheLuaSurfaceWithTheSameValues(t *testing.T) {
	source := readFixture(t)
	file, err := ngxParse(t, source)
	require.NoError(t, err, "ngx refused a configuration OpenResty accepts")

	// The Lua bodies, in the order they appear. Each one is a different trap:
	// `;` as a table separator, `if`/`end`, braces inside a string, and the
	// only case with an argument before the body.
	var luaNodes []*config.Node
	var ifs int
	file2 := &config.Tree{Files: []*config.File{file}}
	file2.Walk(func(n *config.Node) bool {
		if config.IsLuaBlockDirective(n.Directive) {
			luaNodes = append(luaNodes, n)
		}
		if n.Directive == "if" {
			ifs++
		}
		return true
	})

	names := make([]string, len(luaNodes))
	for i, n := range luaNodes {
		names[i] = n.Directive
	}
	require.Equal(t, []string{
		"init_by_lua_block",
		"set_by_lua_block",
		"rewrite_by_lua_block",
		"content_by_lua_block",
		"content_by_lua_block",
	}, names)

	// The property that named the original defect: no Lua `if` became an nginx
	// `if` directive. The fixture has three, and none of them is nginx's.
	require.Zero(t, ifs, "an `if` from inside Lua was read as an nginx directive")

	// No Lua block opened a directive block: the body is an ARGUMENT.
	for _, n := range luaNodes {
		require.Falsef(t, n.HasBlock(), "%s opened a directive block", n.Directive)
	}

	// The body, byte for byte. The argument span covers the whole lexeme,
	// braces included -- the same rule as the quotes of a quoted argument --
	// so the text pointed at is always "{" + Args[last] + "}".
	for _, n := range luaNodes {
		require.Lenf(t, n.ArgSpans, len(n.Args), "%s: one span per argument", n.Directive)
		body := n.Args[len(n.Args)-1]
		require.Equalf(t, "{"+body+"}", text(file, n.ArgSpans[len(n.ArgSpans)-1]),
			"%s: the body span does not point at the body", n.Directive)
	}

	// init_by_lua_block: `;` separating table fields, and braces inside a
	// string. Read as configuration, they would become directives.
	require.Len(t, luaNodes[0].Args, 1)
	require.Contains(t, luaNodes[0].Args[0], `local cfg = { limit = 10; name = "a; b { c }" }`)
	require.Contains(t, luaNodes[0].Args[0], "if cfg.limit > 0 then")

	// set_by_lua_block is the only one with an argument BEFORE the body, and
	// that argument is read as a run of non-spaces, with no notion of quotes.
	require.Len(t, luaNodes[1].Args, 2, "set_by_lua_block has the variable and the body")
	require.Equal(t, "$mark", luaNodes[1].Args[0])
	require.Equal(t, "$mark", text(file, luaNodes[1].ArgSpans[0]))
	require.Contains(t, luaNodes[1].Args[1], `return "two; { items }"`)

	// The single-line body, exactly: it is short enough that there is no
	// excuse for an approximation.
	require.Equal(t, []string{` ngx.say("ok; {1}") `}, luaNodes[4].Args)

	// And what this whole effort protects: the directive AFTER the block. If
	// our token stream desynchronises from crossplane's, this is where it
	// shows -- silently, with spans pointing at the wrong text.
	for _, want := range []struct{ directive, span string }{
		{"keepalive_timeout", "keepalive_timeout 65;"},
		{"add_header", "add_header X-Mark $mark;"},
		{"access_log", "access_log off;"},
	} {
		var found *config.Node
		file2.Walk(func(n *config.Node) bool {
			if found == nil && n.Directive == want.directive {
				found = n
			}
			return true
		})
		require.NotNilf(t, found, "directive %s vanished from the tree", want.directive)
		require.Equal(t, want.span, text(file, found.Span))
		require.Equal(t, want.directive, text(file, found.HeadSpan)[:len(want.directive)])
	}
}

// Where ngx and the real binary AGREE on block delimitation.
//
// This table used to be called divergences, and every row in it recorded a
// shape ngx refused and OpenResty accepted. All four came from the same cause:
// crossplane's Lua lexer counts braces outside `'` and `"` and knows nothing
// about escapes, long brackets or comments.
//
// They are agreements now because ngx stopped depending on that lexer and
// registers its own (internal/config/luascan.go). The rows are kept, inverted,
// for the reason the old ones existed: each is a shape that was measured
// against a real binary rather than reasoned about, and if a future change
// hands delimitation back to the dependency, these go red instead of quietly
// reintroducing the refusal of valid OpenResty configuration.
func TestNgxAgreesWithOpenRestyOnBlockDelimitation(t *testing.T) {
	requireOracle(t)

	const template = "events { worker_connections 16; }\n" +
		"http { server { listen 8080; location / {\n" +
		"content_by_lua_block {%s}\n" +
		"access_log off;\n" +
		"} } }\n"

	cases := []struct {
		name string
		body string
		why  string
	}{
		{
			name: "escaped single quote",
			body: ` local s = 'a\'b' `,
			why: "Lua accepts \\' inside a single-quoted string. crossplane's lexer " +
				"treated the backslash as any other character, so the second quote " +
				"closed the string, the closing brace fell 'inside quotes', and the " +
				"block never ended.",
		},
		{
			name: "escaped double quote",
			body: ` local s = "a\"b" `,
			why:  "Same cause, other quote.",
		},
		{
			name: "long bracket with an unbalanced brace",
			body: ` local s = [[ } ]] `,
			why: "A Lua long bracket is a string. To crossplane's lexer it was not, so " +
				"the brace inside it counted and the block closed early.",
		},
		{
			name: "Lua comment with an unbalanced brace",
			body: " -- }\n ngx.say(1) ",
			why:  "Same cause: the dependency's lexer did not know Lua comments.",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := strings.Replace(template, "%s", c.body, 1)

			output, accepted := openrestyTest(t, src)
			require.Truef(t, accepted,
				"OpenResty refused %q, so this row no longer describes valid "+
					"configuration.\n%s\n%s", c.name, c.why, output)

			file, err := ngxParse(t, src)
			require.NoErrorf(t, err,
				"ngx refused what OpenResty accepts for %q -- delimitation "+
					"regressed.\n%s", c.name, c.why)

			// Accepting is not enough. A delimiter placed one byte off can
			// still produce a tree, with the directive after the block eaten
			// into the Lua body, so the proof is that the directive survived.
			tree := &config.Tree{Files: []*config.File{file}}
			var found bool
			tree.Walk(func(n *config.Node) bool {
				if n.Directive == "access_log" {
					found = true
				}
				return true
			})
			require.Truef(t, found,
				"the directive after the block was swallowed for %q, so the body "+
					"ended in the wrong place even though the file parsed", c.name)
		})
	}
}

// The one divergence still standing, and it is semantic rather than a matter of
// delimitation: lua-nginx-module refuses an empty body with "no runnable Lua
// code", a judgement about what the code DOES. ngx delimits the block
// correctly and says nothing about its contents, which is the same thing
// `openresty -t` does for every non-empty body.
//
// It stays open on purpose. Closing it would mean ngx deciding whether Lua is
// runnable, which is a different tool.
func TestTheRemainingDivergenceIsSemanticAndNotDelimitation(t *testing.T) {
	requireOracle(t)

	src := "events { worker_connections 16; }\n" +
		"http { server { listen 8080; location / {\n" +
		"content_by_lua_block {}\n" +
		"access_log off;\n" +
		"} } }\n"

	output, accepted := openrestyTest(t, src)
	require.Falsef(t, accepted,
		"OpenResty now accepts an empty Lua body; this note has aged:\n%s", output)
	require.Contains(t, output, "no runnable Lua code",
		"OpenResty still refuses it, but for a different reason than recorded")

	_, err := ngxParse(t, src)
	require.NoError(t, err,
		"ngx started refusing an empty body -- delimiting an empty block is "+
			"correct, and refusing it would be a semantic judgement ngx does not make")
}

// The divergence that used to block v0.2, asserted from the other side.
//
// `content_by_lua_block { -- }` is a comment that swallows the closing brace,
// so the block never ends and the file is truncated. OpenResty refuses it.
// crossplane's lexer closed the block at that brace, which made ngx ACCEPT the
// file and build a tree describing a structure the running server never had --
// with nothing in the output saying so. An edit targeted at that tree would
// have cut in the wrong place, which is why v0.2 could not start.
//
// Both refuse it now, and this test is what keeps that true.
func TestNgxRefusesTheCommentedOutBraceJustAsOpenRestyDoes(t *testing.T) {
	requireOracle(t)

	src := "events { worker_connections 16; }\n" +
		"http { server { listen 8080; location / {\n" +
		"content_by_lua_block { -- }\n" +
		"access_log off; }\n" +
		"} }\n"

	output, accepted := openrestyTest(t, src)
	require.Falsef(t, accepted,
		"OpenResty now accepts this; the note has aged:\n%s", output)

	_, err := ngxParse(t, src)
	require.Error(t, err,
		"ngx accepts a file OpenResty refuses, which is the v0.2 blocker "+
			"reopening: the tree would describe a structure the server never had")
}
