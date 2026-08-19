package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/config"
)

// The fixture behind this test is accepted by real nginx 1.20.1 -- verified in
// the container, and re-verified on every integration run. That matters more
// than it looks: accepting what nginx accepts is the whole contract, and the
// only way to be sure is to ask nginx.
//
// The assertions are about VALUES, not about acceptance. Parsing a file
// without error while mangling a quoted argument is a worse failure than
// refusing it, because nothing downstream can tell.
func TestSuperficieDeSintaxeDoNginx(t *testing.T) {
	tree, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "syntax_surface.conf"),
	})
	require.NoError(t, err, "nginx accepts this file; so must we")

	achado := map[string][][]string{}
	var anda func(ns []*config.Node)
	anda = func(ns []*config.Node) {
		for _, n := range ns {
			achado[n.Directive] = append(achado[n.Directive], n.Args)
			anda(n.Block)
		}
	}
	anda(tree.Files[0].Nodes)

	casos := []struct {
		nome      string
		directive string
		indice    int
		esperado  []string
	}{
		{"a quoted value keeps its semicolon and braces", "add_header", 0,
			[]string{"X-Um", "valor; com { chaves } e ponto-virgula"}},
		{"single quotes work like double ones", "add_header", 1,
			[]string{"X-Dois", "outro; valor"}},
		{"escaped quotes are unescaped exactly once", "add_header", 2,
			[]string{"X-Tres", `com "aspas" escapadas`}},
		{"variables stay literal, both forms", "add_header", 3,
			[]string{"X-Quatro", "variaveis $host e ${host}"}},
		{"an exact-match location keeps its operator apart", "location", 0,
			[]string{"=", "/exato"}},
		{"a regex location keeps its backslashes", "location", 2,
			[]string{"~", `\.php$`}},
		{"an escaped space does not split the path", "location", 5,
			[]string{`/com\ espaco`}},
		{"a negated file test survives as one argument", "if", 2,
			[]string{"!-f", "$request_filename"}},
		{"an empty string argument is preserved", "if", 3,
			[]string{"$arg_x", "!=", ""}},
		{"a compound time unit is one argument", "keepalive_timeout", 0,
			[]string{"1h30m"}},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			ocorrencias := achado[c.directive]
			require.Greater(t, len(ocorrencias), c.indice,
				"the fixture no longer has enough %q directives", c.directive)
			assert.Equal(t, c.esperado, ocorrencias[c.indice])
		})
	}

	// The map body is not made of directives -- it is free key/value pairs, and
	// a regex key, an empty key and a backslash key are exactly where a
	// tokeniser that treats them as directives goes wrong.
	var corpoDoMap []string
	for _, n := range tree.Files[0].Nodes {
		anda2(n, &corpoDoMap)
	}
	assert.Contains(t, corpoDoMap, "~*bot|crawler")
	assert.Contains(t, corpoDoMap, `\d+`)
	assert.Contains(t, corpoDoMap, "", "the empty-string key has to survive")
}

func anda2(n *config.Node, out *[]string) {
	if n.Directive == "map" {
		for _, f := range n.Block {
			*out = append(*out, f.Directive)
		}
		return
	}
	for _, f := range n.Block {
		anda2(f, out)
	}
}
