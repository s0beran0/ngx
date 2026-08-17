package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/eduardoborges/ngx/internal/cli"
	"github.com/eduardoborges/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

func TestFlagInvalidaProduzExitDeUso(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--flag-que-nao-existe"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)
}

func TestComandoDesconhecidoProduzExitDeUso(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"comando-inexistente"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)
}

// --json e --human sao mutuamente exclusivos; pedir os dois e erro de uso,
// nao uma precedencia silenciosa.
func TestJSONEHumanJuntosSaoErroDeUso(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--json", "--human", "version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)
}

func TestVersionSaiNoEnvelopeJSONSemTTY(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitOK, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	require.True(t, env.OK)
	require.Equal(t, "version", env.Command)
}

// O erro precisa sair no envelope, no stdout, para o agente conseguir ler.
// Escrever so no stderr obrigaria o agente a capturar dois streams.
func TestErroDeExecucaoSaiNoEnvelope(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--json", "--human", "version"}, &out, &errBuf, false)
	require.Equal(t, output.ExitUsage, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	require.False(t, env.OK)
	require.NotEmpty(t, env.Diagnostics)
	require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
}

// mudaDiretorio troca o cwd do processo para dir e restaura o original ao
// final do teste. LocalSettingsPath (".ngx/config.yaml") e relativo ao cwd,
// entao e assim que um teste consegue controlar qual arquivo local o
// Execute carrega sem tocar em caminhos absolutos do sistema.
func mudaDiretorio(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(wd))
	})
}

// Um output.format invalido no arquivo de configuracao e erro de uso mesmo
// com --quiet: a validacao roda no preparar, antes do portao de quiet do
// Renderer, entao o erro nunca fica em silencio so porque o usuario pediu
// saida minima.
func TestFormatoInvalidoNaConfigEhErroDeUsoMesmoComQuiet(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".ngx"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".ngx", "config.yaml"),
		[]byte("output:\n  format: xlm\n"),
		0o644,
	))
	mudaDiretorio(t, dir)

	var out, errBuf bytes.Buffer
	code := cli.Execute([]string{"--quiet", "version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	require.False(t, env.OK)
	require.NotEmpty(t, env.Diagnostics)
	require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
	require.Contains(t, env.Diagnostics[0].Message, "xlm")
}
