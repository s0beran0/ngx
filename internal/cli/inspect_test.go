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
	_, _, raw := runInspect(t, "inspect", "--full-tree", "-c", fixture(t))

	require.Contains(t, raw, `"id":"h.s0"`)
	require.Contains(t, raw, `"id":"h.s0.l0"`)
	require.Contains(t, raw, `"id":"h.u0"`)
}

func TestInspectEmitsSpans(t *testing.T) {
	_, _, raw := runInspect(t, "inspect", "--full-tree", "-c", fixture(t))

	require.Contains(t, raw, `"span"`)
	require.Contains(t, raw, `"head_span"`)
}

// The test that closes the redaction loop: the sensitive value cannot show up
// in the output, but the directive must.
func TestInspectRedactsThePrivateKey(t *testing.T) {
	_, _, raw := runInspect(t, "inspect", "--full-tree", "-c", fixture(t))

	require.NotContains(t, raw, "/etc/ssl/private/api.key")
	require.Contains(t, raw, "ssl_certificate_key", "the directive stays visible")
	require.Contains(t, raw, output.RedactedValue)
}

// inspectNode is the reader's view of a node: the fields this file needs, read
// out of the JSON by their published names, so the test breaks if a tag moves.
type inspectNode struct {
	Directive    string        `json:"directive"`
	Args         []string      `json:"args"`
	RedactedArgs []int         `json:"redacted_args"`
	Block        []inspectNode `json:"block"`
}

// directivesNamed collects every node with the given name from the rendered
// tree, in document order.
func directivesNamed(t *testing.T, raw, name string) []inspectNode {
	t.Helper()

	var response struct {
		Data struct {
			Config []struct {
				Parsed []inspectNode `json:"parsed"`
			} `json:"config"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))

	var found []inspectNode
	var walk func(nodes []inspectNode)
	walk = func(nodes []inspectNode) {
		for _, n := range nodes {
			if n.Directive == name {
				found = append(found, n)
			}
			walk(n.Block)
		}
	}
	for _, f := range response.Data.Config {
		walk(f.Parsed)
	}
	return found
}

// The test the marker exists for. The fixture holds a real secret AND an
// argument that is literally "***": both come out as "***" in the JSON, so the
// string alone cannot answer whether the value was censored or is the content
// itself. Only redacted_args separates them -- a fixture with a secret alone
// would pass with the defect still in place.
func TestInspectDistinguishesARedactedValueFromALiteralAsterisks(t *testing.T) {
	_, _, raw := runInspect(t, "inspect", "--full-tree", "-c", filepath.Join("testdata", "redaction.conf"))

	require.NotContains(t, raw, "s3cr3t-token", "the secret cannot reach the output")

	headers := directivesNamed(t, raw, "proxy_set_header")
	require.Len(t, headers, 2)

	censored, literal := headers[0], headers[1]
	require.Equal(t, []string{"Authorization", output.RedactedValue}, censored.Args)
	require.Equal(t, []string{"X-Masked-Upstream", output.RedactedValue}, literal.Args)
	require.Equal(t, censored.Args[1], literal.Args[1],
		"the two values are the same string: the text cannot be what tells them apart")

	require.Equal(t, []int{1}, censored.RedactedArgs)
	require.Nil(t, literal.RedactedArgs,
		"a value the configuration really holds must not be reported as redacted")
}

// The mark points at the argument, not at the directive: the header name stays
// readable, which is what says WHICH header was censored.
func TestInspectRedactsTheArgumentWithoutCollapsingTheOthers(t *testing.T) {
	_, _, raw := runInspect(t, "inspect", "--full-tree", "-c", filepath.Join("testdata", "redaction.conf"))

	keys := directivesNamed(t, raw, "ssl_certificate_key")
	require.Len(t, keys, 1)
	require.Equal(t, []string{output.RedactedValue}, keys[0].Args)
	require.Equal(t, []int{0}, keys[0].RedactedArgs)

	// An untouched directive carries no mark at all.
	pass := directivesNamed(t, raw, "proxy_pass")
	require.Len(t, pass, 1)
	require.Nil(t, pass[0].RedactedArgs)
	require.NotContains(t, raw, `"redacted_args":[]`,
		"an empty list would say something was redacted when nothing was")
}

// The schema version has to be in the FAILURE envelope too: an agent that only
// ever sees errors still has to know which shape it is reading.
func TestInspectFailureEnvelopeCarriesTheSchemaVersion(t *testing.T) {
	code, _, raw := runInspect(t, "inspect", "-c", filepath.Join("testdata", "invalid.conf"))
	require.Equal(t, output.ExitInvalidConfig, code)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &decoded))
	require.Equal(t, float64(output.SchemaVersion), decoded["schema_version"])
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
	code, _, raw := runInspect(t, "inspect", "--full-tree", "--combine",
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
