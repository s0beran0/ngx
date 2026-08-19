//go:build integration

// Package bench_test valida a bancada de Lua. Ele fica fora de
// internal/config de proposito: a fixture que ele usa e um ARTEFATO DA
// BANCADA, nao de um pacote -- ela so tem sentido quando existe um binario
// com lua-nginx-module para responder por ela, e o `make bench-lua-up` que
// sobe esse binario mora aqui.
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

// O container que `make bench-lua-up` sobe.
const luaCT = "ngx-bench-lua"

// fixtureLua e a superficie de sintaxe Lua, o par de
// internal/config/testdata/syntax_surface.conf.
const fixtureLua = "testdata/lua_surface.conf"

// exigeOraculo pula o teste quando a bancada de Lua nao esta de pe. Pular e
// mais honesto que falhar: quem roda `go test -tags integration ./...` sem
// docker nao tem defeito nenhum no codigo.
func exigeOraculo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker nao esta disponivel; suba a bancada para rodar isto")
	}
	if err := exec.Command("docker", "inspect", luaCT).Run(); err != nil {
		t.Skip("a bancada de Lua nao esta de pe: rode `make bench-lua-up`")
	}
}

// openrestyTest roda `openresty -t` de verdade sobre src, dentro do container,
// e devolve a saida combinada e se o binario aceitou.
//
// Uma medida que vale registrar, porque delimita o que este oraculo prova:
// `openresty -t` NAO compila o corpo Lua. `content_by_lua_block { if end }` e
// ate `{ isto nao e lua !!! }` passam. O modulo so lexa o corpo para achar
// onde ele TERMINA -- que e exatamente a pergunta que o ngx tambem responde.
// Entao o oraculo cobre a delimitacao do bloco, e nada alem dela.
func openrestyTest(t *testing.T, src string) (string, bool) {
	t.Helper()

	copiar := exec.Command("docker", "exec", "-i", luaCT,
		"sh", "-c", "cat > /tmp/oracle.conf")
	copiar.Stdin = strings.NewReader(src)
	require.NoError(t, copiar.Run(), "nao consegui colocar a configuracao no container")

	saida, err := exec.Command("docker", "exec", luaCT,
		"openresty", "-t", "-c", "/tmp/oracle.conf").CombinedOutput()
	return string(saida), err == nil
}

// ngxParse tenta parsear src com o ngx e devolve o arquivo e o erro.
func ngxParse(t *testing.T, src string) (*config.File, error) {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "ngx.conf")
	require.NoError(t, os.WriteFile(caminho, []byte(src), 0o644))
	tree, err := config.Parse(config.ParseOptions{Path: caminho})
	if err != nil {
		return nil, err
	}
	require.Len(t, tree.Files, 1)
	return tree.Files[0], nil
}

func lerFixture(t *testing.T) string {
	t.Helper()
	dados, err := os.ReadFile(fixtureLua)
	require.NoError(t, err)
	return string(dados)
}

func texto(f *config.File, s config.Span) string { return string(f.Source[s.Start:s.End]) }

// O oraculo que faltava. Ate aqui, tudo que o projeto afirmava sobre aceitar
// configuracao OpenResty se apoiava no lexer do crossplane e em raciocinio
// sobre sintaxe Lua -- sem binario nenhum para dizer o contrario. Este teste e
// o binario dizendo.
func TestSuperficieLuaEhAceitaPeloOpenRestyReal(t *testing.T) {
	exigeOraculo(t)

	saida, ok := openrestyTest(t, lerFixture(t))
	require.Truef(t, ok,
		"o OpenResty real recusou a fixture, entao ela deixou de ser uma "+
			"descricao de configuracao valida:\n%s", saida)
	require.Contains(t, saida, "syntax is ok")
}

// A outra metade: o ngx le a MESMA fixture e chega nos mesmos valores. Sem
// isto, o teste acima provaria apenas que a fixture e valida, e nao que nos
// concordamos com quem a valida.
func TestSuperficieLuaEhLidaPeloNgxComOsMesmosValores(t *testing.T) {
	fonte := lerFixture(t)
	file, err := ngxParse(t, fonte)
	require.NoError(t, err, "o ngx recusou uma configuracao que o OpenResty aceita")

	// Os corpos Lua, na ordem em que aparecem. Cada um e uma armadilha
	// diferente: `;` como separador de tabela, `if`/`end`, chaves dentro de
	// string, e o unico caso com argumento antes do corpo.
	var luas []*config.Node
	var ifs int
	file2 := &config.Tree{Files: []*config.File{file}}
	file2.Walk(func(n *config.Node) bool {
		if config.IsLuaBlockDirective(n.Directive) {
			luas = append(luas, n)
		}
		if n.Directive == "if" {
			ifs++
		}
		return true
	})

	nomes := make([]string, len(luas))
	for i, n := range luas {
		nomes[i] = n.Directive
	}
	require.Equal(t, []string{
		"init_by_lua_block",
		"set_by_lua_block",
		"rewrite_by_lua_block",
		"content_by_lua_block",
		"content_by_lua_block",
	}, nomes)

	// A propriedade que da nome ao defeito original: nenhum `if` do Lua virou
	// diretiva `if` do nginx. A fixture tem tres, e nenhum e do nginx.
	require.Zero(t, ifs, "um `if` de dentro do Lua foi lido como diretiva nginx")

	// Nenhum bloco Lua abriu um bloco de diretivas: o corpo e ARGUMENTO.
	for _, n := range luas {
		require.Falsef(t, n.HasBlock(), "%s abriu um bloco de diretivas", n.Directive)
	}

	// O corpo, byte a byte. O span do argumento cobre o lexema inteiro,
	// chaves incluidas -- a mesma regra das aspas de um argumento aspado --,
	// entao o texto apontado e sempre "{" + Args[ultimo] + "}".
	for _, n := range luas {
		require.Lenf(t, n.ArgSpans, len(n.Args), "%s: um span por argumento", n.Directive)
		corpo := n.Args[len(n.Args)-1]
		require.Equalf(t, "{"+corpo+"}", texto(file, n.ArgSpans[len(n.ArgSpans)-1]),
			"%s: o span do corpo nao aponta para o corpo", n.Directive)
	}

	// init_by_lua_block: `;` separando campos de tabela e chaves dentro de
	// string. Lidos como configuracao, virariam diretivas.
	require.Len(t, luas[0].Args, 1)
	require.Contains(t, luas[0].Args[0], `local cfg = { limite = 10; nome = "a; b { c }" }`)
	require.Contains(t, luas[0].Args[0], "if cfg.limite > 0 then")

	// set_by_lua_block e o unico com argumento ANTES do corpo, e o argumento
	// e lido como uma sequencia de nao-espacos, sem nocao de aspas.
	require.Len(t, luas[1].Args, 2, "set_by_lua_block tem a variavel e o corpo")
	require.Equal(t, "$marca", luas[1].Args[0])
	require.Equal(t, "$marca", texto(file, luas[1].ArgSpans[0]))
	require.Contains(t, luas[1].Args[1], `return "dois; { itens }"`)

	// O corpo de uma linha so, exato: e curto o bastante para nao haver
	// desculpa para aproximacao.
	require.Equal(t, []string{` ngx.say("ok; {1}") `}, luas[4].Args)

	// E o que este trabalho inteiro protege: a diretiva DEPOIS do bloco. Se o
	// nosso fluxo de tokens dessincronizar do fluxo do crossplane, e aqui que
	// aparece -- em silencio, com spans apontando para o texto errado.
	for _, esperado := range []struct{ diretiva, span string }{
		{"keepalive_timeout", "keepalive_timeout 65;"},
		{"add_header", "add_header X-Marca $marca;"},
		{"access_log", "access_log off;"},
	} {
		var achou *config.Node
		file2.Walk(func(n *config.Node) bool {
			if achou == nil && n.Directive == esperado.diretiva {
				achou = n
			}
			return true
		})
		require.NotNilf(t, achou, "diretiva %s sumiu da arvore", esperado.diretiva)
		require.Equal(t, esperado.span, texto(file, achou.Span))
		require.Equal(t, esperado.diretiva, texto(file, achou.HeadSpan)[:len(esperado.diretiva)])
	}
}

// As DIVERGENCIAS entre o oraculo e o ngx, medidas e nao supostas.
//
// Elas ficam registradas como teste, e nao so como prosa, por um motivo: se
// um dia o crossplane consertar o lexer de Lua upstream, ou o lua-nginx-module
// mudar de ideia, este teste fica vermelho e alguem revisa a nota. Uma
// divergencia documentada em markdown envelhece em silencio.
//
// Consertar qualquer uma delas esta FORA do escopo de quem escreveu este
// arquivo: o defeito, quando existe, e do lexer da dependencia, e esta base ja
// recusou uma vez a saida de forkar o crossplane.
func TestDivergenciasEntreOraculoENgx(t *testing.T) {
	exigeOraculo(t)

	const molde = "events { worker_connections 16; }\n" +
		"http { server { listen 8080; location / {\n" +
		"content_by_lua_block {%s}\n" +
		"access_log off;\n" +
		"} } }\n"

	casos := []struct {
		nome      string
		corpo     string
		openresty bool // o binario aceita?
		ngx       bool // o ngx aceita?
		porque    string
	}{
		{
			nome:      "aspas simples escapadas",
			corpo:     ` local s = 'a\'b' `,
			openresty: true,
			ngx:       false,
			porque: "DIVERGENCIA. Lua aceita \\' dentro de string simples; o lexer do " +
				"crossplane (lua.go) trata a barra invertida como um caractere qualquer, " +
				"entao a segunda aspa FECHA a string e a chave que fecha o bloco cai " +
				"'dentro de aspas'. O bloco nunca termina e o arquivo e recusado.",
		},
		{
			nome:      "aspas duplas escapadas",
			corpo:     ` local s = "a\"b" `,
			openresty: true,
			ngx:       false,
			porque:    "Mesma causa da anterior: a barra invertida nao escapa nada no lexer da dependencia.",
		},
		{
			nome:      "corpo vazio",
			corpo:     "",
			openresty: false,
			ngx:       true,
			porque: "DIVERGENCIA NA OUTRA DIRECAO, e semantica, nao sintatica: o " +
				"lua-nginx-module recusa com 'no runnable Lua code'. Nao e um defeito " +
				"do ngx -- delimitar um bloco vazio esta certo --, mas e a razao de a " +
				"fixture nao ter um.",
		},
		{
			nome:      "colchete longo com chave desbalanceada",
			corpo:     ` local s = [[ } ]] `,
			openresty: true,
			ngx:       false,
			porque: "O colchete longo do Lua nao e string para o lexer do crossplane: a " +
				"chave de dentro dele conta, e o bloco fecha cedo demais.",
		},
		{
			nome:      "comentario Lua com chave desbalanceada",
			corpo:     " -- }\n ngx.say(1) ",
			openresty: true,
			ngx:       false,
			porque:    "Mesma causa: o lexer da dependencia nao conhece comentario Lua.",
		},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			src := strings.Replace(molde, "%s", c.corpo, 1)

			saida, aceitou := openrestyTest(t, src)
			require.Equalf(t, c.openresty, aceitou,
				"o OpenResty mudou de comportamento para %q.\n%s\n%s", c.nome, c.porque, saida)

			_, err := ngxParse(t, src)
			if c.ngx {
				require.NoErrorf(t, err, "o ngx mudou de comportamento para %q.\n%s", c.nome, c.porque)
				return
			}
			require.Errorf(t, err,
				"o ngx passou a aceitar %q -- se foi upstream, a nota abaixo envelheceu.\n%s",
				c.nome, c.porque)
		})
	}
}

// A divergencia mais grave das medidas, e a unica que nao cabe no molde acima
// porque depende do texto que vem DEPOIS do bloco.
//
// Um comentario Lua com uma chave desbalanceada faz o lexer do crossplane
// fechar o bloco CEDO. Nos casos da tabela acima isso vira erro, o que e ruim
// mas visivel. Aqui nao: o ngx ACEITA o arquivo e monta uma arvore, enquanto o
// OpenResty o RECUSA. Ou seja, o ngx descreve uma estrutura que o servidor real
// nunca teve -- e sem nenhum sinal para quem consome a saida.
//
// Continua fora do escopo consertar: a delimitacao vem do lexer da dependencia.
// Fica registrado aqui para que a nota nao envelheca em silencio.
func TestComentarioLuaComChaveFazNgxAceitarOQueOOpenRestyRecusa(t *testing.T) {
	exigeOraculo(t)

	src := "events { worker_connections 16; }\n" +
		"http { server { listen 8080; location / {\n" +
		"content_by_lua_block { -- }\n" +
		"access_log off; }\n" +
		"} }\n"

	saida, aceitou := openrestyTest(t, src)
	require.Falsef(t, aceitou, "o OpenResty passou a aceitar isto; a nota envelheceu:\n%s", saida)

	_, err := ngxParse(t, src)
	require.NoError(t, err,
		"o ngx passou a recusar isto -- se foi upstream, a divergencia fechou e "+
			"esta nota pode sair")
}
