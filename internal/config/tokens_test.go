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

// Fix round 1 -- Critical: "${" e tratado pelo crossplane como expansao de
// parametro e fica dentro da mesma palavra. Sem isso, "http://${backend}"
// vira quatro tokens com "{" e "}" fantasmas, e a Task 9 rejeita o arquivo
// inteiro no primeiro proxy_pass com variavel de template Docker/envsubst.
func TestExpansaoDeParametroNaoQuotadaFicaNumSoToken(t *testing.T) {
	toks, err := config.Tokenize([]byte(`proxy_pass http://${backend};`))
	require.NoError(t, err)

	require.Len(t, toks, 3, "http://${backend} precisa ser um unico token, sem { e } fantasmas")
	require.Equal(t, "proxy_pass", toks[0].Value)
	require.Equal(t, config.TokenWord, toks[1].Kind)
	require.Equal(t, "http://${backend}", toks[1].Value)
	require.Equal(t, "http://${backend}", toks[1].Raw)
	require.Equal(t, config.TokenSemicolon, toks[2].Kind)
}

// Expansao de parametro tambem funciona dentro de aspas, onde ela e so mais
// um caractere do valor (nao ha estado inVar dentro de aspas).
func TestExpansaoDeParametroDentroDeAspas(t *testing.T) {
	toks, err := config.Tokenize([]byte(`set $a "${b}c";`))
	require.NoError(t, err)

	require.Equal(t, "${b}c", toks[2].Value)
	require.True(t, toks[2].Quoted)
}

// Fix round 1 -- Important: o conjunto de espacos tem que bater com o do
// crossplane (strings.TrimSpace / unicode.IsSpace), nao so os quatro bytes
// ascii. NBSP entra em .conf por copia de documentacao web e e invisivel;
// sem esse ajuste a contagem de argumentos diverge da do crossplane.
func TestConjuntoDeEspacosCobreNBSPTabVerticalEFormFeed(t *testing.T) {
	src := []byte("listen 80;\vserver_name\fa;")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	var valores []string
	for _, tok := range toks {
		valores = append(valores, tok.Value)
	}
	require.Equal(t, []string{"listen", "80", ";", "server_name", "a", ";"}, valores,
		"NBSP, tab vertical e form feed tem que separar argumentos, igual ao crossplane")
}

// Fix round 1 -- Important: o CR de um terminador CRLF nao pode entrar no
// span (nem no Value) de um comentario. Se entrasse, reescrever esse
// comentario na v0.2 apagaria o CR e converteria a linha de CRLF para LF --
// uma mudanca fora do alvo que o projeto promete nunca fazer.
func TestComentarioCRLFExcluiCRDoSpanEDoValue(t *testing.T) {
	src := []byte("# comentario\r\nlisten 80;\r\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Equal(t, config.TokenComment, toks[0].Kind)
	require.Equal(t, "# comentario", toks[0].Raw, "o CR fica de fora do span do comentario")
	require.Equal(t, " comentario", toks[0].Value)
	require.Equal(t, string(src[toks[0].Start:toks[0].End]), toks[0].Raw)

	require.Equal(t, "listen", toks[1].Value)
	require.Equal(t, 2, toks[1].Line, "a segunda linha comeca depois do CRLF")
}

// Fix round 1 -- Important: so a barra que precede a aspa delimitadora e
// desescapada; qualquer outro escape fica literal em Value, igual ao
// crossplane. msg "a\nb"; produz Value a\nb (barra e n literais), nao uma
// quebra de linha real.
func TestEscapeDentroDeAspasSoDesescapaAAspaAtiva(t *testing.T) {
	toks, err := config.Tokenize([]byte(`msg "a\nb";`))
	require.NoError(t, err)

	require.Equal(t, `a\nb`, toks[1].Value,
		"so a barra antes da aspa e removida; o resto do escape fica literal")
}

// Fix round 1 -- Important: Column conta runes, nao bytes -- e a posicao
// visual que um editor mostraria. Start continua obrigatoriamente em bytes.
func TestColumnContaRunesNaoBytes(t *testing.T) {
	src := []byte(`msg "çãé";`)

	toks, err := config.Tokenize(src)
	require.NoError(t, err)
	require.Len(t, toks, 3)

	require.Equal(t, config.TokenSemicolon, toks[2].Kind)
	require.Equal(t, 12, toks[2].Start, "start continua contando bytes (ç, ã, e sao 2 bytes cada)")
	require.Equal(t, 10, toks[2].Column, "column conta runes: e a posicao que um editor mostraria")
}

// Achado do fuzz no fix round 1: uma barra invertida solta no ultimo byte
// do arquivo (sem par de escape possivel) e engolida pelo crossplane --
// nao produz token nenhum. Sem esse tratamento, viravamos um token fantasma
// "\" que o crossplane nunca produz, desalinhando a contagem na Task 9.
func TestBarraInvertidaNoFimDoArquivoNaoGeraTokenFantasma(t *testing.T) {
	toks, err := config.Tokenize([]byte(`foo \`))
	require.NoError(t, err)

	require.Len(t, toks, 1, "a barra final solta nao deve gerar token nenhum")
	require.Equal(t, "foo", toks[0].Value)
}

// Achado do fuzz no fix round 1: um \r solto (sem \n depois) no meio de uma
// palavra nao-quotada e invisivel para o crossplane -- nunca termina a
// palavra. So um \n de verdade termina. Sem isso, "0\r0" virava dois tokens
// em vez de um, desalinhando a contagem na Task 9.
func TestBarraCRSoltaNoMeioDaPalavraNaoTerminaAPalavra(t *testing.T) {
	toks, err := config.Tokenize([]byte("0\r0;"))
	require.NoError(t, err)

	require.Len(t, toks, 2)
	require.Equal(t, "00", toks[0].Value, "o \\r solto fica invisivel, nao separa os dois digitos")
	require.Equal(t, config.TokenSemicolon, toks[1].Kind)
}

// Achado do fuzz no fix round 1: uma barra invertida seguida de \r, quando
// forma a palavra inteira (nada mais para tokenizar), nao produz token
// nenhum -- igual ao crossplane, que tambem engole os dois sem nunca
// mesclar nada com o esc pendente.
func TestBarraSeguidaDeCRSemMaisNadaNaoGeraToken(t *testing.T) {
	toks, err := config.Tokenize([]byte(" \\\r"))
	require.NoError(t, err)
	require.Empty(t, toks)
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
