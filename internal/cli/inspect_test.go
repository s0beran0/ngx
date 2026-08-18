package cli_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/cli"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

func runInspect(t *testing.T, args ...string) (output.ExitCode, *output.Envelope, string) {
	t.Helper()
	var out, errBuf bytes.Buffer

	code := cli.Execute(args, &out, &errBuf, false)

	var env output.Envelope
	if out.Len() > 0 {
		require.NoError(t, json.Unmarshal(out.Bytes(), &env), "output: %s", out.String())
	}
	return code, &env, out.String()
}

func fixture(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "example.conf")
}

func TestInspectRetornaSucesso(t *testing.T) {
	code, env, _ := runInspect(t, "inspect", "-c", fixture(t))

	require.Equal(t, output.ExitOK, code)
	require.True(t, env.OK)
	require.Equal(t, "inspect", env.Command)
}

// The hash in meta is the anchor of the IDs that go out in data.
func TestInspectPublishesTheConfigHashInMeta(t *testing.T) {
	_, env, _ := runInspect(t, "inspect", "-c", fixture(t))

	require.NotEmpty(t, env.Meta.ConfigHash)
	require.Contains(t, env.Meta.ConfigHash, "sha256:")
}

func TestInspectSummarizesTheConfiguration(t *testing.T) {
	_, _, raw := runInspect(t, "inspect", "-c", fixture(t))

	var response struct {
		Data struct {
			Summary cli.Summary `json:"summary"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))

	require.Equal(t, 1, response.Data.Summary.Servers)
	require.Equal(t, 2, response.Data.Summary.Locations)
	require.Equal(t, 1, response.Data.Summary.Upstreams)
	require.Equal(t, 1, response.Data.Summary.Files)
}

// The IDs have to go out in the JSON: they are how the agent references a
// node on the next call.
func TestInspectEmitsIDsInTheTree(t *testing.T) {
	_, _, raw := runInspect(t, "inspect", "-c", fixture(t))

	require.Contains(t, raw, `"id":"h.s0"`)
	require.Contains(t, raw, `"id":"h.s0.l0"`)
	require.Contains(t, raw, `"id":"h.u0"`)
}

func TestInspectEmitsSpans(t *testing.T) {
	_, _, raw := runInspect(t, "inspect", "-c", fixture(t))

	require.Contains(t, raw, `"span"`)
	require.Contains(t, raw, `"head_span"`)
}

// The test that closes the redaction loop: the sensitive value cannot show up
// in the output, but the directive must.
func TestInspectRedactsThePrivateKey(t *testing.T) {
	_, _, raw := runInspect(t, "inspect", "-c", fixture(t))

	require.NotContains(t, raw, "/etc/ssl/private/api.key")
	require.Contains(t, raw, "ssl_certificate_key", "the directive stays visible")
	require.Contains(t, raw, output.RedactedValue)
}

// A nonexistent file is an IO failure, not a usage error: the flag was
// correct, it was the disk that did not have the file.
func TestInspectWithNonexistentFileIsAnInternalFailure(t *testing.T) {
	code, env, _ := runInspect(t, "inspect", "-c", "testdata/does-not-exist.conf")

	require.Equal(t, output.ExitInternal, code)
	require.False(t, env.OK)
}

func TestInspectWithNoConfigAtAllIsUsageError(t *testing.T) {
	code, env, _ := runInspect(t, "inspect")

	require.Equal(t, output.ExitUsage, code)
	require.False(t, env.OK)
}

func TestInspectCombineResolveIncludes(t *testing.T) {
	code, _, raw := runInspect(t, "inspect", "--combine",
		"-c", filepath.Join("..", "config", "testdata", "combine", "nginx.conf"))

	require.Equal(t, output.ExitOK, code)
	require.Contains(t, raw, `"origin"`)
	require.NotContains(t, raw, `"directive":"include"`,
		"the include was resolved and no longer appears in the tree")
}

// Invalid configuration (a syntax error) is exit 3 -- output.InvalidConfig --
// not exit 1: the one that got it wrong was the user's .conf, not ngx itself.
// The diagnostic has to carry the file and line of the problem, inherited from
// the config.ParseErrors that config.Parse returns, instead of a single
// message with no location.
func TestInspectWithInvalidSyntaxIsConfigErrorWithLocatedDiagnostic(t *testing.T) {
	code, env, _ := runInspect(t, "inspect", "-c", filepath.Join("testdata", "invalid.conf"))

	require.Equal(t, 3, int(code), "exit code from the invalid configuration contract")
	require.False(t, env.OK)

	require.Len(t, env.Diagnostics, 1)
	d := env.Diagnostics[0]
	require.Equal(t, "invalid.conf", filepath.Base(d.File))
	require.NotZero(t, d.Line)
	require.NotEmpty(t, d.Message)
}

// "if () { ... }" used to take the process down inside crossplane
// (prepareIfArgs, util.go:83): no envelope, no useful exit code, with the
// dependency's stack trace on stderr -- the worst possible output for a
// consumer that reads stdout as JSON. The contract here is the same as for any
// invalid syntax: envelope on stdout, exit 3 and a located diagnostic.
func TestInspectWithIfWithoutExpressionDoesNotBringTheProcessDown(t *testing.T) {
	code, env, raw := runInspect(t, "inspect", "-c", filepath.Join("testdata", "if_empty.conf"))

	require.Equal(t, 3, int(code))
	require.False(t, env.OK)
	require.NotEmpty(t, raw, "the output has to be an envelope, not a panic")
	require.Len(t, env.Diagnostics, 1)
	require.Equal(t, "if_empty.conf", filepath.Base(env.Diagnostics[0].File))
	require.Equal(t, 3, env.Diagnostics[0].Line)
}
