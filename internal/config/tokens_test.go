package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"unicode"

	"github.com/eduardoborges/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

// A invariante que sustenta todo o resto: o texto entre Start e End precisa
// ser exatamente o Raw do token. Se isso vale, os spans sao confiaveis.
func TestTokenSpansApontamParaOTextoOriginal(t *testing.T) {
	src := []byte("server {\n    listen 443 ssl;\n}\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	for _, tok := range toks {
		require.Equal(t, tok.Raw, string(src[tok.Start:tok.End]),
			"token %q em [%d,%d)", tok.Value, tok.Start, tok.End)
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

// Aspas escondem ; e { do tokenizador. Errar isso quebra o alinhamento
// inteiro no primeiro add_header com ponto e virgula dentro.
func TestAspasProtegemDelimitadores(t *testing.T) {
	src := []byte(`add_header X-A "b; c { d }";`)

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Len(t, toks, 4)
	require.Equal(t, "add_header", toks[0].Value)
	require.Equal(t, "X-A", toks[1].Value)
	require.Equal(t, "b; c { d }", toks[2].Value, "o valor vem sem as aspas")
	require.Equal(t, `"b; c { d }"`, toks[2].Raw, "o raw mantem as aspas")
	require.True(t, toks[2].Quoted)
	require.Equal(t, config.TokenSemicolon, toks[3].Kind)
}

func TestAspasSimplesTambemFuncionam(t *testing.T) {
	toks, err := config.Tokenize([]byte(`return 200 'ok; fim';`))
	require.NoError(t, err)

	require.Equal(t, "ok; fim", toks[2].Value)
	require.True(t, toks[2].Quoted)
}

func TestEscapeDentroDeAspas(t *testing.T) {
	toks, err := config.Tokenize([]byte(`msg "diz \"oi\"";`))
	require.NoError(t, err)

	require.Equal(t, `diz "oi"`, toks[1].Value)
}

func TestComentarioVaiAteOFimDaLinha(t *testing.T) {
	src := []byte("# um comentario; com ponto e virgula\nlisten 80;\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Equal(t, config.TokenComment, toks[0].Kind)
	require.Equal(t, "# um comentario; com ponto e virgula", toks[0].Raw)
	require.Equal(t, " um comentario; com ponto e virgula", toks[0].Value,
		"o valor do comentario vem sem o # inicial")
	require.Equal(t, "listen", toks[1].Value)
}

func TestLinhaEColunaSaoBaseUm(t *testing.T) {
	src := []byte("server {\n    listen 80;\n}\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Equal(t, 1, toks[0].Line)
	require.Equal(t, 1, toks[0].Column)

	// "listen" comeca na segunda linha, apos quatro espacos.
	require.Equal(t, "listen", toks[2].Value)
	require.Equal(t, 2, toks[2].Line)
	require.Equal(t, 5, toks[2].Column)
}

func TestAspasNaoFechadasVirarErro(t *testing.T) {
	_, err := config.Tokenize([]byte(`msg "sem fim;`))

	require.Error(t, err)
}

// Cobertura: todo byte que nao e espaco em branco pertence a algum token.
func TestTokensCobremTodoByteSignificativo(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "simples.conf"))
	require.NoError(t, err)

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	coberto := make([]bool, len(src))
	prev := 0
	for _, tok := range toks {
		require.GreaterOrEqual(t, tok.Start, prev, "tokens fora de ordem")
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
			"byte %d (%q) nao coberto e nao e espaco", i, string(b))
	}
}
