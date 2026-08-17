package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

// escrever grava src num arquivo temporario e devolve o caminho.
func escrever(t *testing.T, src string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.conf")
	require.NoError(t, os.WriteFile(p, []byte(src), 0o644))
	return p
}

// recusa roda Parse e devolve o primeiro ParseError, exigindo que a recusa
// seja tipada -- e nao um erro solto, que a CLI traduziria para exit 1.
func recusa(t *testing.T, src string) config.ParseError {
	t.Helper()
	tree, err := config.Parse(config.ParseOptions{Path: escrever(t, src)})
	require.Error(t, err)
	require.Nil(t, tree)

	var problemas config.ParseErrors
	require.True(t, errors.As(err, &problemas), "recusa precisa ser ParseErrors (exit 3), erro foi: %v", err)
	require.NotEmpty(t, problemas)
	return problemas[0]
}

// aceitaNoCrossplane documenta, executando, que a entrada e aceita pela
// dependencia. E o que torna a recusa do ngx uma DIVERGENCIA enumerada, e nao
// uma sobre-rejeicao acidental: cada teste que usa este helper corresponde a
// uma entrada de divergenciasConhecidas em fuzz_test.go.
func aceitaNoCrossplane(t *testing.T, path string) {
	t.Helper()
	payload, err := crossplane.Parse(path, &crossplane.ParseOptions{
		ParseComments:             true,
		CombineConfigs:            false,
		SingleFile:                false,
		SkipDirectiveArgsCheck:    true,
		SkipDirectiveContextCheck: true,
		ErrorOnUnknownDirectives:  false,
	})
	require.NoError(t, err)
	require.Equal(t, "ok", payload.Status)
}

// Defeito 1 da Task 9 (CRITICO). Com SkipDirectiveArgsCheck ligado
// (parse.go), a guarda validExpr de analyze.go:212 nao roda, e prepareIfArgs
// (util.go:71-86) chega em d.Args[1:0] na linha 83: slice bounds out of range.
// O processo morria com stack trace da dependencia -- para um consumidor que
// le o stdout como JSON, a pior saida possivel.
func TestIfComExpressaoVaziaEhRecusaTipadaENaoPanic(t *testing.T) {
	for _, src := range []string{
		"if () { return 404; }\n",
		"if (){}\n",
		"http { server { if () { return 404; } } }\n",
		"if ( ) {}\n",
		"if $a {}\n",
	} {
		t.Run(src, func(t *testing.T) {
			pe := recusa(t, src)
			require.Equal(t, config.RecusaExpressaoIfInvalida, pe.Classe)
			require.NotZero(t, pe.Line)
		})
	}
}

// A expressao valida continua passando: a guarda replicada nao pode recusar
// nada que o nginx aceite.
func TestIfComExpressaoValidaContinuaAceito(t *testing.T) {
	for _, src := range []string{
		"http { server { if ($a = b) { return 404; } } }\n",
		"http { server { if ( $a = b ) { return 404; } } }\n",
		"http { server { if ($http_user_agent ~ MSIE) { return 404; } } }\n",
		"http { server { if (!-f $request_filename) { return 404; } } }\n",
	} {
		t.Run(src, func(t *testing.T) {
			tree, err := config.Parse(config.ParseOptions{Path: escrever(t, src)})
			require.NoError(t, err)
			require.NotNil(t, tree)
		})
	}
}

// Dentro de um corpo map-like o crossplane nem chega em prepareIfArgs
// (parse.go:304-321 faz continue antes de analyze), entao um "if ()" ali e um
// parametro de map como outro qualquer. Recusar seria sobre-rejeicao.
func TestIfDentroDeCorpoMapLikeNaoEValidado(t *testing.T) {
	src := "map $a $b {\n  if ();\n}\n"
	p := escrever(t, src)
	aceitaNoCrossplane(t, p)

	_, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)
}

// Defeito 2 da Task 9 (ALTO). Num corpo map-like, o comentario que aparece no
// meio dos argumentos e coletado pelo crossplane (parse.go:289) mas nunca
// emitido: parse.go:319 faz continue antes do laco de parse.go:436. Sem no
// "#" para reclamar a fila, o comentario avulso seguinte casava com o token
// errado e o ngx recusava um arquivo que o nginx e o crossplane aceitam.
func TestComentarioNosArgumentosDeCorpoMapLike(t *testing.T) {
	casos := map[string]string{
		"map com comentario avulso depois":   "map $a $b {\n  default # x\n  0;\n  # real\n}\n",
		"map com dois comentarios avulsos":   "map $a $b {\n  default # x\n  0;\n  # r1\n  # r2\n}\n",
		"map sem comentario avulso":          "map $a $b {\n  default # x\n  0;\n}\n",
		"types":                              "types {\n  text/html # t\n  html;\n}\n",
		"split_clients":                      "split_clients $a $b {\n  0.5% # c\n  x;\n  # depois\n}\n",
		"geo dentro de http":                 "http {\n  geo $a {\n    default # c\n    0;\n    # depois\n  }\n}\n",
		"bloco normal depois de um map-like": "map $a $b {\n  default # x\n  0;\n}\nserver_name a # y\n  b;\n# depois\n",
	}
	for nome, src := range casos {
		t.Run(nome, func(t *testing.T) {
			p := escrever(t, src)
			aceitaNoCrossplane(t, p)

			tree, err := config.Parse(config.ParseOptions{Path: p})
			require.NoError(t, err)
			require.NotNil(t, tree)
		})
	}
}

// Defeito 3 da Task 9 (MEDIO). Uma diretiva cujo NOME e o texto citado "#"
// chega com Directive == "#" e Comment == nil. O crossplane distingue com
// !IsQuoted (parse.go:264); IsComment precisa do mesmo criterio, senao o
// aligner entra no ramo de comentario e ou consome um TokenComment que nao
// existe ou saca da fila o span de um comentario anterior.
func TestDiretivaComNomeHashCitadoNaoEComentario(t *testing.T) {
	casos := []string{
		"\"#\" a;\n",
		"\"#\" a { }\n",
		"d 1 # c\n;\n\"#\" b;\n",
		"map $a $b {\n  \"#\" 1;\n}\n",
	}
	for _, src := range casos {
		t.Run(src, func(t *testing.T) {
			p := escrever(t, src)
			aceitaNoCrossplane(t, p)

			tree, err := config.Parse(config.ParseOptions{Path: p})
			require.NoError(t, err)

			var achou bool
			tree.Walk(func(n *config.Node) bool {
				if n.Directive == "#" && n.Comment == nil {
					achou = true
					require.False(t, n.IsComment(), "diretiva citada nao e comentario")
				}
				return true
			})
			require.True(t, achou, "a fixture precisa ter a diretiva de nome \"#\"")
		})
	}
}

// Defeito 4 da Task 9 (BAIXO com peso alto). HeadSpan pode conter um
// comentario ("default # x\n 0"), e a v0.2 reescreve HeadSpan por
// substituicao de bytes: sem registro, essa reescrita apagaria um comentario
// do usuario. HeadComments torna isso visivel na arvore.
func TestHeadSpanRegistraComentariosInternos(t *testing.T) {
	src := "map $a $b {\n  default # x\n  0;\n}\n"
	tree, err := config.Parse(config.ParseOptions{Path: escrever(t, src)})
	require.NoError(t, err)

	var def *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "default" {
			def = n
		}
		return true
	})
	require.NotNil(t, def)

	fonte := tree.Files[0].Source
	require.Contains(t, string(fonte[def.HeadSpan.Start:def.HeadSpan.End]), "# x")
	require.Len(t, def.HeadComments, 1)
	require.Equal(t, "# x", string(fonte[def.HeadComments[0].Start:def.HeadComments[0].End]))
	require.GreaterOrEqual(t, def.HeadComments[0].Start, def.HeadSpan.Start)
	require.LessOrEqual(t, def.HeadComments[0].End, def.HeadSpan.End)
}

// Um comentario DEPOIS do ultimo argumento fica fora de HeadSpan e nao pode
// ser registrado: registra-lo seria mentir sobre o intervalo.
func TestComentarioForaDaCabecaNaoERegistrado(t *testing.T) {
	src := "server_name a # y\n  ;\n"
	tree, err := config.Parse(config.ParseOptions{Path: escrever(t, src)})
	require.NoError(t, err)
	require.Empty(t, tree.Files[0].Nodes[0].HeadComments)
}

// --- divergencias enumeradas contra o crossplane -------------------------
//
// Cada teste abaixo corresponde a exatamente uma entrada de
// divergenciasConhecidas (fuzz_test.go). Sao as unicas recusas do ngx que o
// oraculo do fuzz aceita sem falhar.

// O lexer do crossplane fecha a aspa implicitamente no fim do arquivo
// (lex.go:325-327) e nao emite token nenhum quando o conteudo esta vazio, o
// que faz uma aspa solta virar Status "ok" com zero diretivas. O nginx
// recusa; nos recusamos junto.
func TestDivergenciaAspaNaoFechada(t *testing.T) {
	// So a aspa VAZIA no fim da fonte diverge: lex.go:325-327 emite o token
	// quando token.Len() > 0, entao "a \"b" ou "\"\n" viram um token quotado
	// que deixa o statement sem terminador e o crossplane devolve Status
	// "failed" -- ali nao ha divergencia, ele tambem recusa.
	for _, src := range []string{`"`, `'`, "server {}\n\""} {
		t.Run(src, func(t *testing.T) {
			p := escrever(t, src)
			aceitaNoCrossplane(t, p)

			_, err := config.Parse(config.ParseOptions{Path: p})
			require.Error(t, err)
			var problemas config.ParseErrors
			require.True(t, errors.As(err, &problemas))
			require.Equal(t, config.RecusaAspaNaoFechada, problemas[0].Classe)
		})
	}
}

// O crossplane monta o statement com t.Value sem checar que o primeiro token
// e uma palavra (parse.go:256-261), entao "{}" vira para ele uma diretiva
// chamada "{". O nginx recusa; nos recusamos, registrando o token exato.
func TestDivergenciaChaveComoNomeDeDiretiva(t *testing.T) {
	p := escrever(t, "{}\n")
	aceitaNoCrossplane(t, p)

	pe := recusa(t, "{}\n")
	require.Equal(t, config.RecusaTokenNoLugarDeDiretiva, pe.Classe)
	require.Equal(t, "{", pe.Token)
}

// Mesma raiz do teste acima, outro token: ";0;" vira para o crossplane uma
// diretiva chamada ";" com o argumento "0". Achado pelo fuzz depois da lista
// enumerada entrar em vigor -- que e exatamente o efeito de nao silenciar a
// classe inteira por substring da mensagem.
func TestDivergenciaPontoEVirgulaComoNomeDeDiretiva(t *testing.T) {
	p := escrever(t, ";0;\n")
	aceitaNoCrossplane(t, p)

	pe := recusa(t, ";0;\n")
	require.Equal(t, config.RecusaTokenNoLugarDeDiretiva, pe.Classe)
	require.Equal(t, ";", pe.Token)
}

// O laco de argumentos do crossplane para em "}" (parse.go:285) e a mensagem
// "is not terminated by \";\"" (analyze.go:224-227) nao roda com
// SkipDirectiveArgsCheck, entao "server { listen 80 }" e aceito por ele. O
// nginx recusa; nos recusamos, registrando o "}".
func TestDivergenciaDiretivaSemPontoEVirgula(t *testing.T) {
	p := escrever(t, "server { listen 80 }\n")
	aceitaNoCrossplane(t, p)

	pe := recusa(t, "server { listen 80 }\n")
	require.Equal(t, config.RecusaTerminadorAusente, pe.Classe)
	require.Equal(t, "}", pe.Token)
}

// O crossplane so confere que o alvo explicito de um include abre
// (parse.go:385-395, "nginx will check that the included file can be opened
// and read") -- e abrir diretorio abre. O alvo entra em fnames, e lexado no
// laco de parse.go:161-168, o lexer engole o erro de leitura e o payload sai
// com Status "ok" e zero diretiva. O nginx le o alvo e falha. Achado pelo
// fuzz depois da cobertura de include (A8) entrar; o erro cru do Go
// ("read ...: is a directory") vazava para o diagnostico.
func TestDivergenciaIncludeDeDiretorio(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	p := filepath.Join(dir, "f.conf")
	require.NoError(t, os.WriteFile(p, []byte("include sub;\n"), 0o644))
	aceitaNoCrossplane(t, p)

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.Error(t, err)
	require.Nil(t, tree)

	var problemas config.ParseErrors
	require.True(t, errors.As(err, &problemas), "erro cru do Go nao pode vazar: %v", err)
	require.Equal(t, config.RecusaAlvoNaoERegular, problemas[0].Classe)
	require.Equal(t, filepath.Join(dir, "sub"), problemas[0].File)
	require.NotContains(t, problemas[0].Message, "is a directory",
		"a mensagem e nossa, nao a string de erro do runtime")
}

// A mesma classe nao pode disparar para arquivo regular: e o que a mantem
// estreita o bastante para ser enumerada sem token exato.
func TestIncludeDeArquivoRegularContinuaAceito(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub.conf"), []byte("listen 80;\n"), 0o644))
	p := filepath.Join(dir, "f.conf")
	require.NoError(t, os.WriteFile(p, []byte("include sub.conf;\n"), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)
	require.Len(t, tree.Files, 2)
}

// Mesma divergencia, no ramo do "if", que consome argumentos por posicao do
// terminador: sem parar tambem no "}", a recusa saia como token inesperado --
// a classe reservada para bug do aligner. Entrada achada pelo fuzz.
func TestDivergenciaIfSemTerminadorAntesDeFecharBloco(t *testing.T) {
	src := "a {\n  b { if (c) }\n}\n"
	p := escrever(t, src)
	aceitaNoCrossplane(t, p)

	pe := recusa(t, src)
	require.Equal(t, config.RecusaTerminadorAusente, pe.Classe)
	require.Equal(t, "}", pe.Token)
}
