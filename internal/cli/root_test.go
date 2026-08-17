package cli_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/eduardoborges/ngx/internal/cli"
	"github.com/eduardoborges/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// Uma flag global invalida e um erro cru do cobra, nao um *output.Error: e
// exatamente o ramo de conversao em executar que precisa transformar isso
// num envelope no stdout com NGX-0002, e nao so devolver um exit code sem
// nenhum sinal do que deu errado. comandoDe cai no fallback "ngx" aqui
// porque o cobra nunca chega a resolver qual comando estava sendo executado.
func TestFlagInvalidaProduzExitDeUso(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--flag-que-nao-existe"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	require.False(t, env.OK)
	require.Equal(t, "ngx", env.Command)
	require.NotEmpty(t, env.Diagnostics)
	require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
}

// Mesmo ramo de conversao de erro cru do cobra, agora via comando
// desconhecido em vez de flag invalida.
func TestComandoDesconhecidoProduzExitDeUso(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"comando-inexistente"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	require.False(t, env.OK)
	require.Equal(t, "ngx", env.Command)
	require.NotEmpty(t, env.Diagnostics)
	require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
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
