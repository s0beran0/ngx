package cli_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/s0beran0/ngx/internal/cli"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// An invalid global flag is a raw cobra error, not an *output.Error: it is
// exactly the conversion branch in executar that has to turn this into an
// envelope on stdout with NGX-0002, and not just return an exit code with no
// sign at all of what went wrong. comandoDe falls back to "ngx" here because
// cobra never gets to resolve which command was being run.
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

// The same conversion branch for a raw cobra error, now via an unknown
// command instead of an invalid flag.
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

// --json and --human are mutually exclusive; asking for both is a usage
// error, not a silent precedence.
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

// The error has to go out in the envelope, on stdout, so the agent can read
// it. Writing only to stderr would force the agent to capture two streams.
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
