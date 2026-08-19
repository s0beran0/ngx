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
	require.Contains(t, luaNodes[0].Args[0], `local cfg = { limite = 10; name = "a; b { c }" }`)
	require.Contains(t, luaNodes[0].Args[0], "if cfg.limite > 0 then")

	// set_by_lua_block is the only one with an argument BEFORE the body, and
	// that argument is read as a run of non-spaces, with no notion of quotes.
	require.Len(t, luaNodes[1].Args, 2, "set_by_lua_block has the variable and the body")
	require.Equal(t, "$marca", luaNodes[1].Args[0])
	require.Equal(t, "$marca", text(file, luaNodes[1].ArgSpans[0]))
	require.Contains(t, luaNodes[1].Args[1], `return "dois; { itens }"`)

	// The single-line body, exactly: it is short enough that there is no
	// excuse for an approximation.
	require.Equal(t, []string{` ngx.say("ok; {1}") `}, luaNodes[4].Args)

	// And what this whole effort protects: the directive AFTER the block. If
	// our token stream desynchronises from crossplane's, this is where it
	// shows -- silently, with spans pointing at the wrong text.
	for _, want := range []struct{ directive, span string }{
		{"keepalive_timeout", "keepalive_timeout 65;"},
		{"add_header", "add_header X-Marca $marca;"},
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

// The DIVERGENCES between the oracle and ngx, measured rather than assumed.
//
// They are recorded as a test and not only as prose for one reason: if
// crossplane one day fixes its Lua lexer upstream, or lua-nginx-module changes
// its mind, this test goes red and somebody revisits the note. A divergence
// documented in markdown ages in silence.
//
// Fixing any of them is OUT of scope for whoever wrote this file: the defect,
// where there is one, belongs to the dependency's lexer, and this codebase has
// already once refused the option of forking crossplane.
func TestDivergencesBetweenTheOracleAndNgx(t *testing.T) {
	requireOracle(t)

	const template = "events { worker_connections 16; }\n" +
		"http { server { listen 8080; location / {\n" +
		"content_by_lua_block {%s}\n" +
		"access_log off;\n" +
		"} } }\n"

	cases := []struct {
		name      string
		body      string
		openresty bool // does the binary accept it?
		ngx       bool // does ngx accept it?
		why       string
	}{
		{
			name:      "aspas simples escapadas",
			body:      ` local s = 'a\'b' `,
			openresty: true,
			ngx:       false,
			why: "DIVERGENCIA. Lua aceita \\' dentro de string simples; o lexer do " +
				"crossplane (lua.go) trata a barra invertida como um caractere qualquer, " +
				"entao a segunda aspa FECHA a string e a chave que fecha o bloco cai " +
				"'dentro de aspas'. O bloco nunca termina e o arquivo e recusado.",
		},
		{
			name:      "aspas duplas escapadas",
			body:      ` local s = "a\"b" `,
			openresty: true,
			ngx:       false,
			why:       "Mesma causa da anterior: a barra invertida nao escapa nada no lexer da dependencia.",
		},
		{
			name:      "body vazio",
			body:      "",
			openresty: false,
			ngx:       true,
			why: "DIVERGENCIA NA OUTRA DIRECAO, e semantica, nao sintatica: o " +
				"lua-nginx-module recusa com 'no runnable Lua code'. Nao e um defeito " +
				"do ngx -- delimitar um bloco vazio esta certo --, mas e a razao de a " +
				"fixture nao ter um.",
		},
		{
			name:      "colchete longo com chave desbalanceada",
			body:      ` local s = [[ } ]] `,
			openresty: true,
			ngx:       false,
			why: "O colchete longo do Lua nao e string para o lexer do crossplane: a " +
				"chave de dentro dele conta, e o bloco fecha cedo demais.",
		},
		{
			name:      "comentario Lua com chave desbalanceada",
			body:      " -- }\n ngx.say(1) ",
			openresty: true,
			ngx:       false,
			why:       "Mesma causa: o lexer da dependencia nao conhece comentario Lua.",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := strings.Replace(template, "%s", c.body, 1)

			output, accepted := openrestyTest(t, src)
			require.Equalf(t, c.openresty, accepted,
				"o OpenResty mudou de comportamento para %q.\n%s\n%s", c.name, c.why, output)

			_, err := ngxParse(t, src)
			if c.ngx {
				require.NoErrorf(t, err, "o ngx mudou de comportamento para %q.\n%s", c.name, c.why)
				return
			}
			require.Errorf(t, err,
				"o ngx passou a aceitar %q -- se foi upstream, a nota abaixo envelheceu.\n%s",
				c.name, c.why)
		})
	}
}

// The most serious of the measured divergences, and the only one that does not
// fit the template above, because it depends on the text that comes AFTER the
// block.
//
// A Lua comment holding an unbalanced brace makes crossplane's lexer close the
// block EARLY. In the table cases above that becomes an error, which is bad but
// visible. Not here: ngx ACCEPTS the file and builds a tree, while OpenResty
// REFUSES it. That is, ngx describes a structure the real server never had --
// with no signal at all to whoever consumes the output.
//
// Fixing it stays out of scope: the delimitation comes from the dependency's
// lexer. It is recorded here so the note does not age in silence.
func TestALuaCommentWithABraceMakesNgxAcceptWhatOpenRestyRefuses(t *testing.T) {
	requireOracle(t)

	src := "events { worker_connections 16; }\n" +
		"http { server { listen 8080; location / {\n" +
		"content_by_lua_block { -- }\n" +
		"access_log off; }\n" +
		"} }\n"

	output, accepted := openrestyTest(t, src)
	require.Falsef(t, accepted, "o OpenResty passou a aceitar isto; a nota envelheceu:\n%s", output)

	_, err := ngxParse(t, src)
	require.NoError(t, err,
		"o ngx passou a recusar isto -- se foi upstream, a divergencia fechou e "+
			"esta nota pode sair")
}
