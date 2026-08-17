package cli_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/eduardoborges/ngx/internal/cli"
	"github.com/eduardoborges/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

func rodarInspect(t *testing.T, args ...string) (output.ExitCode, *output.Envelope, string) {
	t.Helper()
	var out, errBuf bytes.Buffer

	code := cli.Execute(args, &out, &errBuf, false)

	var env output.Envelope
	if out.Len() > 0 {
		require.NoError(t, json.Unmarshal(out.Bytes(), &env), "saida: %s", out.String())
	}
	return code, &env, out.String()
}

func fixture(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "exemplo.conf")
}

func TestInspectRetornaSucesso(t *testing.T) {
	code, env, _ := rodarInspect(t, "inspect", "-c", fixture(t))

	require.Equal(t, output.ExitOK, code)
	require.True(t, env.OK)
	require.Equal(t, "inspect", env.Command)
}

// O hash no meta e a ancora dos IDs que saem no data.
func TestInspectPublicaOConfigHashNoMeta(t *testing.T) {
	_, env, _ := rodarInspect(t, "inspect", "-c", fixture(t))

	require.NotEmpty(t, env.Meta.ConfigHash)
	require.Contains(t, env.Meta.ConfigHash, "sha256:")
}

func TestInspectResumeAConfiguracao(t *testing.T) {
	_, _, bruto := rodarInspect(t, "inspect", "-c", fixture(t))

	var resposta struct {
		Data struct {
			Summary cli.Summary `json:"summary"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(bruto), &resposta))

	require.Equal(t, 1, resposta.Data.Summary.Servers)
	require.Equal(t, 2, resposta.Data.Summary.Locations)
	require.Equal(t, 1, resposta.Data.Summary.Upstreams)
	require.Equal(t, 1, resposta.Data.Summary.Files)
}

// Os IDs precisam sair no JSON: e por eles que o agente referencia um no na
// chamada seguinte.
func TestInspectEmiteIDsNaArvore(t *testing.T) {
	_, _, bruto := rodarInspect(t, "inspect", "-c", fixture(t))

	require.Contains(t, bruto, `"id":"h.s0"`)
	require.Contains(t, bruto, `"id":"h.s0.l0"`)
	require.Contains(t, bruto, `"id":"h.u0"`)
}

func TestInspectEmiteSpans(t *testing.T) {
	_, _, bruto := rodarInspect(t, "inspect", "-c", fixture(t))

	require.Contains(t, bruto, `"span"`)
	require.Contains(t, bruto, `"head_span"`)
}

// O teste que fecha o ciclo da redacao: o valor sensivel nao pode aparecer na
// saida, mas a diretiva sim.
func TestInspectRedigeChavePrivada(t *testing.T) {
	_, _, bruto := rodarInspect(t, "inspect", "-c", fixture(t))

	require.NotContains(t, bruto, "/etc/ssl/private/api.key")
	require.Contains(t, bruto, "ssl_certificate_key", "a diretiva continua visivel")
	require.Contains(t, bruto, output.RedactedValue)
}

// Arquivo inexistente e falha de IO, nao erro de uso: a flag estava correta,
// o disco e que nao tinha o arquivo.
func TestInspectComArquivoInexistenteEhFalhaInterna(t *testing.T) {
	code, env, _ := rodarInspect(t, "inspect", "-c", "testdata/nao-existe.conf")

	require.Equal(t, output.ExitInternal, code)
	require.False(t, env.OK)
}

func TestInspectSemNenhumaConfigEhErroDeUso(t *testing.T) {
	code, env, _ := rodarInspect(t, "inspect")

	require.Equal(t, output.ExitUsage, code)
	require.False(t, env.OK)
}

func TestInspectCombineResolveIncludes(t *testing.T) {
	code, _, bruto := rodarInspect(t, "inspect", "--combine",
		"-c", filepath.Join("..", "config", "testdata", "combine", "nginx.conf"))

	require.Equal(t, output.ExitOK, code)
	require.Contains(t, bruto, `"origin"`)
	require.NotContains(t, bruto, `"directive":"include"`,
		"o include foi resolvido e nao aparece mais na arvore")
}

// Configuracao invalida (erro de sintaxe) e exit 3 -- output.InvalidConfig --
// nao exit 1: quem errou foi o .conf do usuario, nao o proprio ngx. O
// diagnostico precisa carregar arquivo e linha do problema, herdados do
// config.ParseErrors que config.Parse devolve, em vez de uma mensagem unica
// sem localizacao.
func TestInspectComSintaxeInvalidaEhErroDeConfiguracaoComDiagnosticoLocalizado(t *testing.T) {
	code, env, _ := rodarInspect(t, "inspect", "-c", filepath.Join("testdata", "invalido.conf"))

	require.Equal(t, 3, int(code), "exit code do contrato de configuracao invalida")
	require.False(t, env.OK)

	require.Len(t, env.Diagnostics, 1)
	d := env.Diagnostics[0]
	require.Equal(t, "invalido.conf", filepath.Base(d.File))
	require.NotZero(t, d.Line)
	require.NotEmpty(t, d.Message)
}
