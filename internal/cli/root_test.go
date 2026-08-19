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
// exactly the conversion branch in execute that has to turn this into an
// envelope on stdout with NGX-0002, and not just return an exit code with no
// sign at all of what went wrong. commandOf falls back to "ngx" here because
// cobra never gets to resolve which command was being run.
func TestInvalidFlagProducesUsageExit(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--flag-that-does-not-exist"}, &out, &errBuf, false)

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
func TestUnknownCommandProducesUsageExit(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"command-inexistente"}, &out, &errBuf, false)

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
func TestJSONAndHumanTogetherAreUsageError(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--json", "--human", "version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)
}

func TestVersionComesOutInTheJSONEnvelopeWithoutTTY(t *testing.T) {
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
func TestExecutionErrorComesOutInTheEnvelope(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--json", "--human", "version"}, &out, &errBuf, false)
	require.Equal(t, output.ExitUsage, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	require.False(t, env.OK)
	require.NotEmpty(t, env.Diagnostics)
	require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
}

// --field prints one raw value from the envelope, for any command, with no
// JSON parser in between.
func TestFieldPrintsOneRawValue(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--field", "data.version", "version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitOK, code)
	require.Equal(t, output.Version+"\n", out.String())
}

// The case that justifies the feature having an exit code of its own: a path
// that does not exist writes NOTHING on stdout. `V=$(ngx --field x.y version)`
// has to fail loudly, never assign an empty string and carry on.
func TestFieldWithMissingPathExitsUsageWithEmptyStdout(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--field", "data.does.not.exist", "version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)
	require.Empty(t, out.String(), "an empty line would become an empty shell variable")
	require.NotEmpty(t, errBuf.String(), "the diagnostic goes to stderr, as any usage error")
}

// --field also works on the error path: the command failed, and the code of
// the diagnostic comes out just the same instead of being swallowed.
func TestFieldReadsTheErrorEnvelope(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute(
		[]string{"--field", "diagnostics.0.code", "inspect", "-c", "/does/not/exist.conf"},
		&out, &errBuf, false,
	)

	require.NotEqual(t, output.ExitOK, code)
	require.Regexp(t, `^NGX-\d{4}\n$`, out.String())
}

// --field with the flags that choose the presentation of the envelope is a
// usage error: asking for one field and for the whole envelope at the same
// time has no coherent answer, and --quiet would suppress exactly the value
// that was asked for.
func TestFieldConflictsWithPresentationFlags(t *testing.T) {
	for _, flag := range []string{"--json", "--human", "--quiet"} {
		t.Run(flag, func(t *testing.T) {
			var out, errBuf bytes.Buffer

			code := cli.Execute([]string{"--field", "ok", flag, "version"}, &out, &errBuf, false)

			require.Equal(t, output.ExitUsage, code)

			// The refusal is about --field itself, so it comes out as a
			// whole envelope: filtering it through the rejected flag
			// would hide the reason for the refusal.
			var env output.Envelope
			require.NoError(t, json.Unmarshal(out.Bytes(), &env))
			require.False(t, env.OK)
			require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
		})
	}
}
