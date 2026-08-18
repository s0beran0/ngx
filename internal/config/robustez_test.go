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

// escrever writes src to a temporary file and returns the path.
func escrever(t *testing.T, src string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.conf")
	require.NoError(t, os.WriteFile(p, []byte(src), 0o644))
	return p
}

// recusa runs Parse and returns the first ParseError, demanding that the
// refusal be typed -- and not a loose error, which the CLI would translate
// into exit 1.
func recusa(t *testing.T, src string) config.ParseError {
	t.Helper()
	tree, err := config.Parse(config.ParseOptions{Path: escrever(t, src)})
	require.Error(t, err)
	require.Nil(t, tree)

	var problemas config.ParseErrors
	require.True(t, errors.As(err, &problemas), "a refusal has to be ParseErrors (exit 3), the error was: %v", err)
	require.NotEmpty(t, problemas)
	return problemas[0]
}

// aceitaNoCrossplane documents, by running it, that the input is accepted by
// the dependency. That is what makes an ngx refusal an enumerated DIVERGENCE
// and not an accidental over-rejection: every test using this helper matches
// one entry of divergenciasConhecidas in fuzz_test.go.
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

// Defect 1 of Task 9 (CRITICAL). With SkipDirectiveArgsCheck turned on
// (parse.go), the validExpr guard of analyze.go:212 does not run, and
// prepareIfArgs (util.go:71-86) reaches d.Args[1:0] on line 83: slice bounds
// out of range. The process died with a stack trace from the dependency --
// for a consumer reading stdout as JSON, the worst possible output.
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

// A valid expression still goes through: the replicated guard must not refuse
// anything nginx accepts.
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

// Inside a map-like body crossplane never even reaches prepareIfArgs
// (parse.go:304-321 does continue before analyze), so an "if ()" in there is a
// map parameter like any other. Refusing it would be over-rejection.
func TestIfDentroDeCorpoMapLikeNaoEValidado(t *testing.T) {
	src := "map $a $b {\n  if ();\n}\n"
	p := escrever(t, src)
	aceitaNoCrossplane(t, p)

	_, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)
}

// Defect 2 of Task 9 (HIGH). In a map-like body, the comment appearing in the
// middle of the arguments is collected by crossplane (parse.go:289) but never
// emitted: parse.go:319 does continue before the loop of parse.go:436. With no
// "#" node to claim the queue, the next standalone comment matched the wrong
// token and ngx refused a file both nginx and crossplane accept.
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

// Defect 3 of Task 9 (MEDIUM). A directive whose NAME is the quoted text "#"
// arrives with Directive == "#" and Comment == nil. Crossplane draws the
// distinction with !IsQuoted (parse.go:264); IsComment needs the same
// criterion, otherwise the aligner takes the comment branch and either
// consumes a TokenComment that does not exist or pulls the span of an earlier
// comment off the queue.
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
					require.False(t, n.IsComment(), "a quoted directive is not a comment")
				}
				return true
			})
			require.True(t, achou, "the fixture has to contain the directive named \"#\"")
		})
	}
}

// Defect 4 of Task 9 (LOW severity, high weight). HeadSpan may contain a
// comment ("default # x\n 0"), and v0.2 rewrites HeadSpan by byte
// replacement: with no record of it, that rewrite would erase a comment the
// user wrote. HeadComments makes it visible in the tree.
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

// A comment AFTER the last argument falls outside HeadSpan and must not be
// recorded: recording it would be lying about the range.
func TestComentarioForaDaCabecaNaoERegistrado(t *testing.T) {
	src := "server_name a # y\n  ;\n"
	tree, err := config.Parse(config.ParseOptions{Path: escrever(t, src)})
	require.NoError(t, err)
	require.Empty(t, tree.Files[0].Nodes[0].HeadComments)
}

// --- enumerated divergences against crossplane ---------------------------
//
// Each test below matches exactly one entry of divergenciasConhecidas
// (fuzz_test.go). These are the only ngx refusals the fuzz oracle accepts
// without failing.

// Crossplane's lexer closes the quote implicitly at end of file
// (lex.go:325-327) and emits no token at all when the content is empty, which
// turns a dangling quote into Status "ok" with zero directives. nginx refuses
// it; we refuse it too.
func TestDivergenciaAspaNaoFechada(t *testing.T) {
	// Only an EMPTY quote at the end of the source diverges: lex.go:325-327
	// emits the token when token.Len() > 0, so "a \"b" or "\"\n" become a
	// quoted token that leaves the statement with no terminator and
	// crossplane returns Status "failed" -- no divergence there, it refuses
	// too.
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

// Crossplane builds the statement out of t.Value without checking that the
// first token is a word (parse.go:256-261), so "{}" becomes for it a directive
// named "{". nginx refuses it; we refuse it, recording the exact token.
func TestDivergenciaChaveComoNomeDeDiretiva(t *testing.T) {
	p := escrever(t, "{}\n")
	aceitaNoCrossplane(t, p)

	pe := recusa(t, "{}\n")
	require.Equal(t, config.RecusaTokenNoLugarDeDiretiva, pe.Classe)
	require.Equal(t, "{", pe.Token)
}

// Same root as the test above, a different token: ";0;" becomes for crossplane
// a directive named ";" with the argument "0". Found by the fuzz after the
// enumerated list came into force -- which is exactly the payoff of not
// silencing the whole class by a substring of the message.
func TestDivergenciaPontoEVirgulaComoNomeDeDiretiva(t *testing.T) {
	p := escrever(t, ";0;\n")
	aceitaNoCrossplane(t, p)

	pe := recusa(t, ";0;\n")
	require.Equal(t, config.RecusaTokenNoLugarDeDiretiva, pe.Classe)
	require.Equal(t, ";", pe.Token)
}

// Crossplane's argument loop stops at "}" (parse.go:285) and the message
// "is not terminated by \";\"" (analyze.go:224-227) does not run under
// SkipDirectiveArgsCheck, so "server { listen 80 }" is accepted by it. nginx
// refuses it; we refuse it, recording the "}".
func TestDivergenciaDiretivaSemPontoEVirgula(t *testing.T) {
	p := escrever(t, "server { listen 80 }\n")
	aceitaNoCrossplane(t, p)

	pe := recusa(t, "server { listen 80 }\n")
	require.Equal(t, config.RecusaTerminadorAusente, pe.Classe)
	require.Equal(t, "}", pe.Token)
}

// Crossplane only checks that the explicit target of an include opens
// (parse.go:385-395, "nginx will check that the included file can be opened
// and read") -- and a directory does open. The target goes into fnames, is
// lexed in the loop of parse.go:161-168, the lexer swallows the read error and
// the payload comes out with Status "ok" and zero directives. nginx reads the
// target and fails. Found by the fuzz after include coverage (A8) landed; the
// raw Go error ("read ...: is a directory") was leaking into the diagnostic.
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
	require.True(t, errors.As(err, &problemas), "the raw Go error must not leak: %v", err)
	require.Equal(t, config.RecusaAlvoNaoERegular, problemas[0].Classe)
	require.Equal(t, filepath.Join(dir, "sub"), problemas[0].File)
	// The guard is about the raw Go error LEAKING, not about the words. In
	// Portuguese the two were easy to tell apart; translated, our message
	// legitimately says "is a directory" too, and the old assertion started
	// failing on a message that was perfectly fine.
	//
	// What actually distinguishes the runtime string is its shape:
	// `read /some/path: is a directory`, carrying the syscall name and the
	// path. That is what must never reach a diagnostic, because it changes
	// between operating systems and Go versions.
	require.NotContains(t, problemas[0].Message, "read "+dir,
		"the raw syscall error must not leak into the message")
	require.NotRegexp(t, `\b(read|open|stat) /`, problemas[0].Message,
		"a diagnostic must not carry a raw syscall error")
}

// The same class must not fire for a regular file: that is what keeps it
// narrow enough to be enumerated without an exact token.
func TestIncludeDeArquivoRegularContinuaAceito(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub.conf"), []byte("listen 80;\n"), 0o644))
	p := filepath.Join(dir, "f.conf")
	require.NoError(t, os.WriteFile(p, []byte("include sub.conf;\n"), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)
	require.Len(t, tree.Files, 2)
}

// The same divergence, in the "if" branch, which consumes arguments by the
// position of the terminator: without stopping at "}" as well, the refusal
// came out as an unexpected token -- the class reserved for aligner bugs.
// Input found by the fuzz.
func TestDivergenciaIfSemTerminadorAntesDeFecharBloco(t *testing.T) {
	src := "a {\n  b { if (c) }\n}\n"
	p := escrever(t, src)
	aceitaNoCrossplane(t, p)

	pe := recusa(t, src)
	require.Equal(t, config.RecusaTerminadorAusente, pe.Classe)
	require.Equal(t, "}", pe.Token)
}
