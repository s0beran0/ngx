package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/config"
)

// The fixture behind every test in this file is testdata/syntax_surface.conf,
// the one real nginx validates in the integration suite. Per-argument spans
// only mean anything against text nginx itself accepts: quotes, escaped
// quotes, regex with backslashes, an escaped space inside a path, an empty
// string argument and a map body with a regex key are exactly where a span
// computed by counting characters goes wrong.
func parseSuperficie(t *testing.T) *config.File {
	t.Helper()
	tree, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "syntax_surface.conf"),
	})
	require.NoError(t, err, "nginx accepts this file; so must we")
	require.Len(t, tree.Files, 1)
	return tree.Files[0]
}

// preOrder flattens the tree in the same order every time, so that two parses
// of two different texts can be compared position by position.
func preOrder(nodes []*config.Node) []*config.Node {
	var out []*config.Node
	var walk func([]*config.Node)
	walk = func(ns []*config.Node) {
		for _, n := range ns {
			out = append(out, n)
			walk(n.Block)
		}
	}
	walk(nodes)
	return out
}

// TestSpansPorArgumentoReproduzemOArgumento is the differential test the plan
// asks for (R5): the oracle is crossplane's Args, the thing under test is our
// span. Slicing the source by the span and running it back through the
// tokenizer has to give exactly one token whose Value is the argument.
//
// It is not the same expression sliced twice: Args comes out of crossplane's
// parser, the span comes out of our aligner, and the tokenizer runs again from
// scratch over the isolated slice. A span off by one byte -- in either
// direction, on either end -- changes the Value that comes back and fails here.
func TestSpansPorArgumentoReproduzemOArgumento(t *testing.T) {
	file := parseSuperficie(t)

	conferidos := 0
	for _, n := range preOrder(file.Nodes) {
		if n.Directive == "if" {
			continue // spans unavailable by design, see TestSpansPorArgumentoAusentesNoIf
		}
		require.NotNil(t, n.ArgSpans, "%q has no arg spans and is not an if", n.Directive)
		require.Len(t, n.ArgSpans, len(n.Args),
			"%q has %d args and %d spans", n.Directive, len(n.Args), len(n.ArgSpans))

		anterior := n.HeadSpan.Start
		for i, s := range n.ArgSpans {
			require.Greater(t, s.Start, n.HeadSpan.Start,
				"span of arg %d of %q starts at the directive name", i, n.Directive)
			require.LessOrEqual(t, s.End, n.HeadSpan.End,
				"span of arg %d of %q runs past the head", i, n.Directive)
			require.GreaterOrEqual(t, s.Start, anterior,
				"span of arg %d of %q overlaps the previous one", i, n.Directive)
			anterior = s.End

			toks, err := config.Tokenize(file.Source[s.Start:s.End])
			require.NoError(t, err, "arg %d of %q does not retokenize", i, n.Directive)
			require.Len(t, toks, 1,
				"arg %d of %q yields %d tokens, expected 1; text=%q",
				i, n.Directive, len(toks), string(file.Source[s.Start:s.End]))
			assert.Equal(t, n.Args[i], toks[0].Value,
				"arg %d of %q: span holds %q, crossplane read %q",
				i, n.Directive, toks[0].Value, n.Args[i])
			conferidos++
		}

		if len(n.ArgSpans) > 0 {
			assert.Equal(t, n.HeadSpan.End, n.ArgSpans[len(n.ArgSpans)-1].End,
				"the head of %q does not end at its last argument", n.Directive)
		}
	}

	// Without this the test would pass on a fixture that lost all its
	// arguments, which is the failure mode a differential test is worst at
	// noticing on its own.
	require.Greater(t, conferidos, 40, "the fixture no longer exercises enough arguments")
}

// TestTheSpanOfAQuotedArgumentCoversTheQuotes pins the decision down in a test
// instead of only in a comment: the span covers the delimiters. It is what
// makes redaction a substitution of the whole lexeme -- "***" written over
// "value; with { braces }" is a valid argument -- instead of a substitution
// inside the quotes, which would leave the delimiters standing and force the
// replacement to be escaped for that quote style.
func TestTheSpanOfAQuotedArgumentCoversTheQuotes(t *testing.T) {
	file := parseSuperficie(t)

	cases := []struct {
		directive  string
		ocorrencia int
		arg        int
		texto      string
		want       string
	}{
		{"add_header", 0, 1, `"value; with { braces } and semicolon"`, "value; with { braces } and semicolon"},
		{"add_header", 1, 1, `'another; value'`, "another; value"},
		{"add_header", 2, 1, `"com \"aspas\" escapadas"`, `com "aspas" escapadas`},
		{"location", 5, 0, `/com\ espaco`, `/com\ espaco`},
		{"location", 2, 1, `\.php$`, `\.php$`},
	}

	vistos := map[string]int{}
	achado := map[string]*config.Node{}
	for _, n := range preOrder(file.Nodes) {
		i := vistos[n.Directive]
		vistos[n.Directive] = i + 1
		achado[n.Directive+"#"+string(rune('0'+i))] = n
	}

	for _, c := range cases {
		n := achado[c.directive+"#"+string(rune('0'+c.ocorrencia))]
		require.NotNil(t, n, "the fixture no longer has %q #%d", c.directive, c.ocorrencia)
		require.Greater(t, len(n.ArgSpans), c.arg)
		s := n.ArgSpans[c.arg]
		assert.Equal(t, c.texto, string(file.Source[s.Start:s.End]),
			"the span of arg %d of %q #%d is not the raw lexeme", c.arg, c.directive, c.ocorrencia)
		assert.Equal(t, c.want, n.Args[c.arg],
			"the value of arg %d of %q #%d changed", c.arg, c.directive, c.ocorrencia)
	}
}

// TestSpansPorArgumentoAusentesNoIf: for "if" there is nothing to record.
// prepareIfArgs (crossplane/util.go:71-86) rewrites Args, so Args[0] can be a
// substring of the lexeme that produced it. The test asserts the absence AND
// asserts that the absence is not laziness -- the lexemes really do not match
// the arguments here.
func TestSpansPorArgumentoAusentesNoIf(t *testing.T) {
	file := parseSuperficie(t)

	ifs := 0
	for _, n := range preOrder(file.Nodes) {
		if n.Directive != "if" {
			continue
		}
		ifs++
		assert.Nil(t, n.ArgSpans, "if must report the spans as unavailable, not guess them")
	}
	require.Equal(t, 4, ifs, "the fixture no longer has the four ifs")

	// The reason, made visible: the first argument of "if ($request_method =
	// POST)" is "$request_method", while the lexeme in the text is
	// "($request_method". Any 1-to-1 span would have to include that
	// parenthesis or invent a trimmed range.
	for _, n := range preOrder(file.Nodes) {
		if n.Directive == "if" && len(n.Args) > 0 && n.Args[0] == "$request_method" {
			head := string(file.Source[n.HeadSpan.Start:n.HeadSpan.End])
			assert.Contains(t, head, "($request_method")
			return
		}
	}
	t.Fatal("the fixture no longer has if ($request_method = POST)")
}

// TestPerArgumentSpansAreAnEmptyListWithNoArguments keeps the two absences apart,
// which is the entire reason the tag is omitzero: [] means "this directive has
// no arguments, iterate away"; the field missing means "the correspondence
// does not exist here, do not assume". omitempty would print neither, and a
// consumer could not tell an "if" from a "types {}".
func TestPerArgumentSpansAreAnEmptyListWithNoArguments(t *testing.T) {
	file := parseSuperficie(t)

	var semArgs, comIf *config.Node
	for _, n := range preOrder(file.Nodes) {
		if n.Directive == "types" && semArgs == nil {
			semArgs = n
		}
		if n.Directive == "if" && comIf == nil {
			comIf = n
		}
	}
	require.NotNil(t, semArgs, "the fixture no longer has types {}")
	require.NotNil(t, comIf)

	require.NotNil(t, semArgs.ArgSpans, "a directive with no arguments gets an empty list, never nil")
	require.Empty(t, semArgs.ArgSpans)

	semArgs.Block = nil // keeps the json small; the block plays no part here
	b, err := json.Marshal(semArgs)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"arg_spans":[]`)

	comIf.Block = nil
	b, err = json.Marshal(comIf)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "arg_spans",
		"an if must omit the field, not publish an empty list")
}

// TestSubstituicaoPorSpanTrocaExatamenteUmArgumento is the rehearsal the plan
// asks for: for EVERY argument of the fixture, overwrite exactly the bytes of
// its span with a sentinel, parse the result again from disk, and demand that
// the new tree be the old one with that one argument changed and nothing else.
//
// This is the test that cannot be tautological. It never compares a span with
// itself: it uses the span to CUT the file, and the verdict comes from a fresh
// full parse -- crossplane's, not ours. A span one byte long in either
// direction glues the sentinel to a neighbouring character, or eats a
// delimiter, and either way some other position of the tree changes. Verified
// by breaking it on purpose: arg.Start-1 and arg.End-1 in align.go both make
// this test fail.
func TestSubstituicaoPorSpanTrocaExatamenteUmArgumento(t *testing.T) {
	const sentinela = "xREDIGIDOx"

	origem, err := os.ReadFile(filepath.Join("testdata", "syntax_surface.conf"))
	require.NoError(t, err)

	original := preOrder(parseSuperficie(t).Nodes)

	type alvo struct {
		no  int
		arg int
	}
	var alvos []alvo
	for i, n := range original {
		for j := range n.ArgSpans {
			alvos = append(alvos, alvo{i, j})
		}
	}
	require.Greater(t, len(alvos), 40, "the fixture no longer exercises enough arguments")

	dir := t.TempDir()
	for _, a := range alvos {
		s := original[a.no].ArgSpans[a.arg]

		mutado := make([]byte, 0, len(origem))
		mutado = append(mutado, origem[:s.Start]...)
		mutado = append(mutado, sentinela...)
		mutado = append(mutado, origem[s.End:]...)

		p := filepath.Join(dir, "mutado.conf")
		require.NoError(t, os.WriteFile(p, mutado, 0o644))

		tree, err := config.Parse(config.ParseOptions{Path: p})
		require.NoError(t, err, "replacing arg %d of %q (%q) broke the file",
			a.arg, original[a.no].Directive, string(origem[s.Start:s.End]))

		novo := preOrder(tree.Files[0].Nodes)
		require.Len(t, novo, len(original),
			"replacing arg %d of %q changed the shape of the tree",
			a.arg, original[a.no].Directive)

		for i := range original {
			require.Equal(t, original[i].Directive, novo[i].Directive,
				"node %d changed its directive while replacing arg %d of %q",
				i, a.arg, original[a.no].Directive)
			esperado := make([]string, len(original[i].Args))
			copy(esperado, original[i].Args)
			if i == a.no {
				esperado[a.arg] = sentinela
			}
			require.Equal(t, esperado, novo[i].Args,
				"node %d (%q) changed while only arg %d of node %d (%q) should have",
				i, original[i].Directive, a.arg, a.no, original[a.no].Directive)
		}
	}
}
