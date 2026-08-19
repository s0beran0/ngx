package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/cli"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// secretConf writes a configuration carrying a real private key path, plus a
// header token, and returns its path. The literal values are what the
// assertions look for: a test that only checks for "***" would pass while the
// secret went out alongside it.
func secretConf(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.conf")
	conf := `events {}

http {
    server {
        listen 443 ssl;
        server_name portal.example.com;
        ssl_certificate_key /etc/ssl/private/portal-SUPER-SECRET.key;

        location / {
            proxy_set_header Authorization "Bearer tok_LEAKED_9f2c";
            proxy_pass http://backend;
        }
    }

    upstream backend {
        server 10.0.0.1:8080;
    }
}
`
	require.NoError(t, os.WriteFile(path, []byte(conf), 0o600))
	return path
}

// THE test of this feature.
//
// Redaction happens in the renderer (D5). If --query ran against the tree in
// memory it would read the REAL value, and the flag would be a way around the
// whole redactor -- an expression pointing straight at the argument would hand
// out the private key that *** exists to hide.
//
// So every expression below aims directly at the secret, by index and by
// search, and every one of them has to come back ***. The literal secret must
// not appear anywhere on stdout.
func TestQueryNeverLeaksARedactedValue(t *testing.T) {
	conf := secretConf(t)

	cases := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "straight at the argument, by index",
			query: ".data.config[0].parsed[1].block[0].block[2].args[0]",
			want:  output.RedactedValue + "\n",
		},
		{
			name:  "found by directive name anywhere in the tree",
			query: `.. | objects | select(.directive == "ssl_certificate_key") | .args[0]`,
			want:  output.RedactedValue + "\n",
		},
		{
			name:  "the whole args list of the directive",
			query: `.. | objects | select(.directive == "ssl_certificate_key") | .args`,
			want:  `["` + output.RedactedValue + `"]` + "\n",
		},
		{
			name: "a header token, where the rule keeps the prefix visible",
			// The rule is "proxy_set_header Authorization", so the
			// header name stays and only the token is redacted.
			query: `.. | objects | select(.directive == "proxy_set_header") | .args`,
			want:  `["Authorization","` + output.RedactedValue + `"]` + "\n",
		},
		{
			name:  "the entire tree serialized by the expression",
			query: ".data.config",
			want:  "", // asserted by absence below, not by equality
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer

			code := cli.Execute(
				[]string{"--query", c.query, "inspect", "--full-tree", "-c", conf},
				&out, &errBuf, false,
			)

			require.Equal(t, output.ExitOK, code, "stderr: %s", errBuf.String())
			if c.want != "" {
				require.Equal(t, c.want, out.String())
			}
			require.NotContains(t, out.String(), "portal-SUPER-SECRET",
				"--query read the tree in memory instead of the redacted envelope")
			require.NotContains(t, out.String(), "tok_LEAKED_9f2c",
				"--query read the tree in memory instead of the redacted envelope")
		})
	}
}

// The counterpart of the test above: with the same expression, --no-redact on
// a terminal is the ONE way to see the real value. Without this the test above
// could pass on a build where the secret never reached the envelope at all,
// and it would be proving nothing.
func TestQueryShowsTheRealValueOnlyWithNoRedactOnATerminal(t *testing.T) {
	conf := secretConf(t)
	query := `.. | objects | select(.directive == "ssl_certificate_key") | .args[0]`

	var out, errBuf bytes.Buffer
	code := cli.Execute(
		[]string{"--query", query, "--no-redact", "inspect", "--full-tree", "-c", conf},
		&out, &errBuf, true, // isTTY: --no-redact is refused off a terminal
	)

	require.Equal(t, output.ExitOK, code, "stderr: %s", errBuf.String())
	require.Equal(t, "/etc/ssl/private/portal-SUPER-SECRET.key\n", out.String())
}

// --no-redact is refused when stdout is not a terminal, and --query does not
// open a hole in that gate.
func TestQueryDoesNotBypassTheNoRedactGate(t *testing.T) {
	conf := secretConf(t)

	var out, errBuf bytes.Buffer
	code := cli.Execute(
		[]string{"--query", ".data", "--no-redact", "inspect", "--full-tree", "-c", conf},
		&out, &errBuf, false,
	)

	require.Equal(t, output.ExitUsage, code)
	require.NotContains(t, out.String(), "portal-SUPER-SECRET")
}

// The case --field cannot answer: a projection over a list. --field addresses
// one value by a fixed path; asking "the paths of every location" needs an
// expression.
func TestQueryProjectsOverAList(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute(
		[]string{
			"--query", `.. | objects | select(.directive == "location") | .args[0]`,
			"inspect", "--full-tree", "-c", fixture(t),
		},
		&out, &errBuf, false,
	)

	require.Equal(t, output.ExitOK, code, "stderr: %s", errBuf.String())
	require.Equal(t, "/\n/health\n", out.String())
}

// A valid expression that matches nothing: exit 0 and a byte-empty stdout.
// The decision is documented in the README and in renderQuery; this is what
// pins it.
func TestQueryWithNoResultsIsSuccessAndWritesNothing(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute(
		[]string{
			"--query", `.. | objects | select(.directive == "there_is_no_such_directive") | .args[0]`,
			"inspect", "--full-tree", "-c", fixture(t),
		},
		&out, &errBuf, false,
	)

	require.Equal(t, output.ExitOK, code)
	require.Empty(t, out.String())
}

// An expression that does not parse is refused BEFORE the command runs, as a
// whole usage envelope on stdout -- not through the broken expression itself,
// which would write no envelope at all.
func TestQueryWithInvalidSyntaxIsUsageEnvelope(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--query", ".data |", "version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env), "output: %s", out.String())
	require.False(t, env.OK)
	require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
	require.Contains(t, env.Diagnostics[0].Message, "--query")
}

// An expression that parses but fails while running is exit 2 with nothing on
// stdout, like --field's missing path: an empty line would be assigned by
// V=$(ngx --query ... status) and the script would carry on believing it
// worked.
func TestQueryRuntimeErrorExitsUsageWithEmptyStdout(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--query", ".command.foo", "version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)
	require.Empty(t, out.String())
	require.NotEmpty(t, errBuf.String(), "the diagnostic goes to stderr, as any usage error")
}

// --query works on the ERROR path too, which is how a script reads what went
// wrong without a JSON parser. The exit code stays the one of the failure, not
// the one of the query.
func TestQueryReadsTheErrorEnvelope(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute(
		[]string{"--query", ".diagnostics[].code", "inspect", "-c", "/does/not/exist.conf"},
		&out, &errBuf, false,
	)

	require.NotEqual(t, output.ExitOK, code)
	require.NotEqual(t, output.ExitUsage, code, "the query must not overwrite the failure's code")
	require.Regexp(t, `^NGX-\d{4}\n$`, out.String())
}

// --query and --field both project the envelope, each in its own shape, and
// there is no coherent answer to being asked for both.
func TestQueryAndFieldTogetherAreUsageError(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--query", ".ok", "--field", "ok", "version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	require.False(t, env.OK)
	require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
}

// The same rule --field follows with the flags that choose the presentation:
// --json/--human ask for the whole envelope at the same time as a projection
// of it, and --quiet would suppress exactly what was asked for.
func TestQueryConflictsWithPresentationFlags(t *testing.T) {
	for _, flag := range []string{"--json", "--human", "--quiet"} {
		t.Run(flag, func(t *testing.T) {
			var out, errBuf bytes.Buffer

			code := cli.Execute([]string{"--query", ".ok", flag, "version"}, &out, &errBuf, false)

			require.Equal(t, output.ExitUsage, code)

			// The refusal is about --query itself, so it comes out as a
			// whole envelope: filtering it through the rejected flag
			// would hide the reason for the refusal.
			var env output.Envelope
			require.NoError(t, json.Unmarshal(out.Bytes(), &env))
			require.False(t, env.OK)
			require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
		})
	}
}

// Nothing about --query is specific to inspect: it lives in the renderer, so
// it answers for every command.
func TestQueryWorksOnAnyCommand(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--query", ".data.version", "version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitOK, code)
	require.Equal(t, output.Version+"\n", out.String())
}
