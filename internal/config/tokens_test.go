package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"unicode"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

// The invariant that holds up everything else: the text between Start and End
// has to be exactly the Raw of the token. If that holds, the spans are
// trustworthy.
func TestTokenSpansApontamParaOTextoOriginal(t *testing.T) {
	src := []byte("server {\n    listen 443 ssl;\n}\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	for _, tok := range toks {
		require.Equal(t, tok.Raw, string(src[tok.Start:tok.End]),
			"token %q at [%d,%d)", tok.Value, tok.Start, tok.End)
	}
}

func TestTokenizeSeparaDelimitadores(t *testing.T) {
	toks, err := config.Tokenize([]byte("server {\n    listen 443;\n}\n"))
	require.NoError(t, err)

	var kinds []config.TokenKind
	var values []string
	for _, tok := range toks {
		kinds = append(kinds, tok.Kind)
		values = append(values, tok.Value)
	}

	require.Equal(t, []string{"server", "{", "listen", "443", ";", "}"}, values)
	require.Equal(t, []config.TokenKind{
		config.TokenWord, config.TokenBlockStart,
		config.TokenWord, config.TokenWord, config.TokenSemicolon,
		config.TokenBlockEnd,
	}, kinds)
}

// Quotes hide ; and { from the tokenizer. Getting this wrong breaks the whole
// alignment at the first add_header with a semicolon inside.
func TestAspasProtegemDelimitadores(t *testing.T) {
	src := []byte(`add_header X-A "b; c { d }";`)

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Len(t, toks, 4)
	require.Equal(t, "add_header", toks[0].Value)
	require.Equal(t, "X-A", toks[1].Value)
	require.Equal(t, "b; c { d }", toks[2].Value, "the value comes without the quotes")
	require.Equal(t, `"b; c { d }"`, toks[2].Raw, "the raw keeps the quotes")
	require.True(t, toks[2].Quoted)
	require.Equal(t, config.TokenSemicolon, toks[3].Kind)
}

func TestAspasSimplesTambemFuncionam(t *testing.T) {
	toks, err := config.Tokenize([]byte(`return 200 'ok; end';`))
	require.NoError(t, err)

	require.Equal(t, "ok; end", toks[2].Value)
	require.True(t, toks[2].Quoted)
}

func TestEscapeDentroDeAspas(t *testing.T) {
	toks, err := config.Tokenize([]byte(`msg "says \"hi\"";`))
	require.NoError(t, err)

	require.Equal(t, `says "hi"`, toks[1].Value)
}

func TestComentarioVaiAteOFimDaLinha(t *testing.T) {
	src := []byte("# a comment; with a semicolon\nlisten 80;\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Equal(t, config.TokenComment, toks[0].Kind)
	require.Equal(t, "# a comment; with a semicolon", toks[0].Raw)
	require.Equal(t, " a comment; with a semicolon", toks[0].Value,
		"the comment value comes without the leading #")
	require.Equal(t, "listen", toks[1].Value)
}

func TestLinhaEColunaSaoBaseUm(t *testing.T) {
	src := []byte("server {\n    listen 80;\n}\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Equal(t, 1, toks[0].Line)
	require.Equal(t, 1, toks[0].Column)

	// "listen" starts on the second line, after four spaces.
	require.Equal(t, "listen", toks[2].Value)
	require.Equal(t, 2, toks[2].Line)
	require.Equal(t, 5, toks[2].Column)
}

func TestAspasNaoFechadasVirarErro(t *testing.T) {
	_, err := config.Tokenize([]byte(`msg "sem fim;`))

	require.Error(t, err)
}

// Fix round 1 -- Critical: crossplane treats "${" as a parameter expansion
// and keeps it inside the same word. Without this, "http://${backend}" turns
// into four tokens with phantom "{" and "}", and Task 9 rejects the whole file
// at the first proxy_pass with a Docker/envsubst template variable.
func TestExpansaoDeParametroNaoQuotadaFicaNumSoToken(t *testing.T) {
	toks, err := config.Tokenize([]byte(`proxy_pass http://${backend};`))
	require.NoError(t, err)

	require.Len(t, toks, 3, "http://${backend} has to be a single token, with no phantom { and }")
	require.Equal(t, "proxy_pass", toks[0].Value)
	require.Equal(t, config.TokenWord, toks[1].Kind)
	require.Equal(t, "http://${backend}", toks[1].Value)
	require.Equal(t, "http://${backend}", toks[1].Raw)
	require.Equal(t, config.TokenSemicolon, toks[2].Kind)
}

// Parameter expansion also works inside quotes, where it is just one more
// character of the value (there is no inVar state inside quotes).
func TestExpansaoDeParametroDentroDeAspas(t *testing.T) {
	toks, err := config.Tokenize([]byte(`set $a "${b}c";`))
	require.NoError(t, err)

	require.Equal(t, "${b}c", toks[2].Value)
	require.True(t, toks[2].Quoted)
}

// Fix round 1 -- Important: the whitespace set has to match crossplane's
// (strings.TrimSpace / unicode.IsSpace), not just the four ascii bytes. NBSP
// gets into a .conf by copying from web documentation and is invisible;
// without this adjustment the argument count diverges from crossplane's.
func TestConjuntoDeEspacosCobreNBSPTabVerticalEFormFeed(t *testing.T) {
	src := []byte("listen 80;\vserver_name\fa;")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	var valores []string
	for _, tok := range toks {
		valores = append(valores, tok.Value)
	}
	require.Equal(t, []string{"listen", "80", ";", "server_name", "a", ";"}, valores,
		"NBSP, vertical tab and form feed have to separate arguments, just like in crossplane")
}

// Fix round 1 -- Important: the CR of a CRLF terminator must not get into the
// span (nor into the Value) of a comment. If it did, rewriting that comment in
// v0.2 would erase the CR and convert the line from CRLF to LF -- an
// off-target change the project promises never to make.
func TestComentarioCRLFExcluiCRDoSpanEDoValue(t *testing.T) {
	src := []byte("# comment\r\nlisten 80;\r\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Equal(t, config.TokenComment, toks[0].Kind)
	require.Equal(t, "# comment", toks[0].Raw, "the CR stays out of the comment span")
	require.Equal(t, " comment", toks[0].Value)
	require.Equal(t, string(src[toks[0].Start:toks[0].End]), toks[0].Raw)

	require.Equal(t, "listen", toks[1].Value)
	require.Equal(t, 2, toks[1].Line, "the second line starts after the CRLF")
}

// Fix round 1 -- Important: only the backslash preceding the delimiting quote
// is unescaped; any other escape stays literal in Value, just like in
// crossplane. msg "a\nb"; yields Value a\nb (a literal backslash and n), not a
// real line break.
func TestEscapeDentroDeAspasSoDesescapaAAspaAtiva(t *testing.T) {
	toks, err := config.Tokenize([]byte(`msg "a\nb";`))
	require.NoError(t, err)

	require.Equal(t, `a\nb`, toks[1].Value,
		"only the backslash before the quote is removed; the rest of the escape stays literal")
}

// Fix round 1 -- Important: Column counts runes, not bytes -- it is the visual
// position an editor would show. Start stays mandatorily in bytes.
func TestColumnContaRunesNaoBytes(t *testing.T) {
	src := []byte(`msg "çãé";`)

	toks, err := config.Tokenize(src)
	require.NoError(t, err)
	require.Len(t, toks, 3)

	require.Equal(t, config.TokenSemicolon, toks[2].Kind)
	require.Equal(t, 12, toks[2].Start, "start keeps counting bytes (c, a, e with a diacritic are 2 bytes each)")
	require.Equal(t, 10, toks[2].Column, "column counts runes: it is the position an editor would show")
}

// Fuzz finding from fix round 1: a stray backslash on the last byte of the
// file (with no possible escape pair) is swallowed by crossplane -- it
// produces no token at all. Without this handling we produced a phantom "\"
// token crossplane never produces, throwing the count of Task 9 off.
func TestBarraInvertidaNoFimDoArquivoNaoGeraTokenFantasma(t *testing.T) {
	toks, err := config.Tokenize([]byte(`foo \`))
	require.NoError(t, err)

	require.Len(t, toks, 1, "the stray trailing backslash must not produce any token")
	require.Equal(t, "foo", toks[0].Value)
}

// Fuzz finding from fix round 1: a stray \r (with no \n after it) in the
// middle of an unquoted word is invisible to crossplane -- it never ends the
// word. Only a real \n ends it. Without this, "0\r0" became two tokens
// instead of one, throwing the count of Task 9 off.
func TestBarraCRSoltaNoMeioDaPalavraNaoTerminaAPalavra(t *testing.T) {
	toks, err := config.Tokenize([]byte("0\r0;"))
	require.NoError(t, err)

	require.Len(t, toks, 2)
	require.Equal(t, "00", toks[0].Value, "the stray \\r stays invisible, it does not split the two digits")
	require.Equal(t, config.TokenSemicolon, toks[1].Kind)
}

// Fuzz finding from fix round 1: a backslash followed by \r, when it makes up
// the whole word (nothing else left to tokenize), produces no token at all --
// just like crossplane, which also swallows both without ever merging anything
// into the pending escape.
func TestBarraSeguidaDeCRSemMaisNadaNaoGeraToken(t *testing.T) {
	toks, err := config.Tokenize([]byte(" \\\r"))
	require.NoError(t, err)
	require.Empty(t, toks)
}

// Fix round 2 -- Important: the CRLF fix in lerComentario was not mirrored in
// lerPalavra nor in lerVar, leaving the CR inside the span of an ordinary
// word. A future rewrite by byte replacement would erase that CR and convert
// the line from CRLF to LF -- the same off-target change the comment case
// already avoids.
func TestPalavraCRLFExcluiCRDoSpan(t *testing.T) {
	src := []byte("proxy_set_header Host\r\n  $host;\r\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Equal(t, "Host", toks[1].Value)
	require.Equal(t, "Host", toks[1].Raw, "the CR of the CRLF must not stay inside the word span")
}

// Fix round 2 -- Important: an explicit regression net for the termination of
// ${...} mode by whitespace. A reviewer mutated that condition and the whole
// suite passed without this test.
func TestExpansaoDeParametroTerminaPorEspaco(t *testing.T) {
	toks, err := config.Tokenize([]byte(`a ${b c;`))
	require.NoError(t, err)

	var valores []string
	for _, tok := range toks {
		valores = append(valores, tok.Value)
	}
	require.Equal(t, []string{"a", "${b c", ";"}, valores)
}

// Coverage: every byte that is not whitespace belongs to some token.
func TestTokensCobremTodoByteSignificativo(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "simples.conf"))
	require.NoError(t, err)

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	coberto := make([]bool, len(src))
	prev := 0
	for _, tok := range toks {
		require.GreaterOrEqual(t, tok.Start, prev, "tokens out of order")
		for i := tok.Start; i < tok.End; i++ {
			coberto[i] = true
		}
		prev = tok.End
	}

	for i, b := range src {
		if coberto[i] {
			continue
		}
		require.True(t, unicode.IsSpace(rune(b)),
			"byte %d (%q) is uncovered and is not whitespace", i, string(b))
	}
}
