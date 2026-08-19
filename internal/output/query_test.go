package output_test

import (
	"bytes"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// THE test of this feature at the renderer level. The expression runs against
// the envelope that WOULD have been printed -- redacted -- and not against the
// data in memory. Pointing --query straight at the sensitive value has to
// return ***, otherwise the flag is a way around the redactor and every
// private key in the configuration comes out with it.
//
// The same property is proved end to end, over a real nginx file, in
// internal/cli/query_test.go.
func TestQueryReadsTheRedactedEnvelope(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Redact: set, Query: ".data.value"}

	env := output.New("get")
	env.Data = redactableData{Value: "/etc/ssl/private/api.key"}
	require.NoError(t, r.Render(env))

	require.Equal(t, output.RedactedValue+"\n", buf.String())
	require.NotContains(t, buf.String(), "api.key")
}

// A string comes out raw, with no quotes: the same rule --field follows, and
// the reason it is the same code.
func TestQueryPrintsScalarRaw(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Query: ".data.version"}

	env := output.New("version")
	env.Data = map[string]string{"version": "1.20.1"}
	require.NoError(t, r.Render(env))

	require.Equal(t, "1.20.1\n", buf.String())
}

// A number keeps its literal: 30000 must not reach the shell as 3e+04. gojq
// understands json.Number, which is why the same document serves both flags.
func TestQueryPrintsNumberWithoutScientificNotation(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Query: ".meta.duration_ms"}

	env := output.New("status")
	env.Meta.DurationMS = 30000
	require.NoError(t, r.Render(env))

	require.Equal(t, "30000\n", buf.String())
}

// An object or a list has no raw form, so it comes out as compact JSON on one
// line -- again the same rule as --field.
func TestQueryPrintsObjectAndListAsCompactJSON(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"object", ".data", "{\"version\":\"1.20.1\"}\n"},
		{"empty list", ".diagnostics", "[]\n"},
		{"list built by the expression", "[.command, .ngx_version]", "[\"version\",\"" + output.Version + "\"]\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Query: c.query}

			env := output.New("version")
			env.Data = map[string]string{"version": "1.20.1"}
			require.NoError(t, r.Render(env))

			require.Equal(t, c.want, buf.String())
		})
	}
}

// gojq may yield several values. One line per value is the contract that makes
// the output readable without a parser -- and what makes "zero lines" mean
// "zero values".
func TestQueryPrintsOneLinePerValue(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Query: ".diagnostics[].code"}

	env := output.New("test")
	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityWarning, Code: "NGX-0004", Message: "a"})
	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityError, Code: "NGX-0003", Message: "b"})
	require.NoError(t, r.Render(env))

	require.Equal(t, "NGX-0004\nNGX-0003\n", buf.String())
}

// A valid expression that matches nothing is exit 0 with a byte-empty stdout,
// and this is the decision the flag documents rather than leaving undefined.
//
// It differs from --field's missing path on purpose. In jq's semantics a wrong
// path yields `null` -- a line -- so nothing is only ever produced by a
// deliberate filter, and "no server matches" is an answer, not a failure.
// Failing on it would break every legitimate zero-match query under `set -e`.
func TestQueryWithNoResultsIsSuccessAndWritesNothing(t *testing.T) {
	for _, query := range []string{
		"empty",
		".diagnostics[]",
		`.diagnostics[] | select(.code == "NGX-9999")`,
	} {
		t.Run(query, func(t *testing.T) {
			var buf bytes.Buffer
			r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Query: query}

			require.NoError(t, r.Render(output.New("status")))

			require.Empty(t, buf.String())
		})
	}
}

// The other half of the same decision: a path that does not exist is NOT
// nothing, it is `null`. That is what keeps a typo from looking like a filter
// that excluded everything.
func TestQueryPrintsNullForAPathThatDoesNotExist(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Query: ".data.nope"}

	require.NoError(t, r.Render(output.New("status")))

	require.Equal(t, "null\n", buf.String())
}

// An expression that does not parse is a usage error, exit 2, carrying gojq's
// own message -- it names the offending token, and rewriting it would make it
// worse.
func TestQueryWithInvalidSyntaxIsUsageError(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Query: ".data |"}

	err := r.Render(output.New("status"))

	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
	require.Contains(t, err.Error(), "--query")
	require.Empty(t, buf.String())
}

// A failure halfway through evaluation must leave stdout byte-empty, not a
// truncated answer: everything is resolved into a buffer before a byte is
// written. Asserting only the exit code would pass with the defect present.
func TestQueryRuntimeErrorWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	// The first value is produced fine; the second indexes a string and
	// blows up. Without the buffer, "NGX-0004" would already be on stdout.
	r := &output.Renderer{
		Out:    &buf,
		Format: output.FormatJSON,
		Query:  `.diagnostics[] | if .severity == "error" then .code.foo else .code end`,
	}

	env := output.New("test")
	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityWarning, Code: "NGX-0004", Message: "a"})
	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityError, Code: "NGX-0003", Message: "b"})
	err := r.Render(env)

	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
	require.Empty(t, buf.String(), "a half-written answer is worse than none")
}

// `halt_error` does not get to choose ngx's exit code: that code says what
// happened to the nginx operation, and an expression overwriting it would make
// a successful read indistinguishable from a failed one.
func TestQueryHaltErrorDoesNotChooseTheExitCode(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Query: `"boom" | halt_error(7)`}

	err := r.Render(output.New("status"))

	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
	require.Empty(t, buf.String())
}

// The --no-redact gate comes first, exactly as it does for --field: --query is
// not a way around it either.
func TestQueryDoesNotBypassTheNoRedactGate(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, NoRedact: true, Query: ".ok"}

	err := r.Render(output.New("get"))

	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
	require.Empty(t, buf.String())
}

// In the renderer, Query takes precedence over the format and over Quiet, for
// the same reason Field does: the conflict is decided at the flag layer,
// because output.format also comes from the configuration file where it is an
// ambient default. Whatever reaches the renderer with Query filled in wants
// the result of the expression.
func TestQueryTakesPrecedenceOverFormatAndQuiet(t *testing.T) {
	for _, format := range []output.Format{output.FormatJSON, output.FormatHuman, output.FormatAuto} {
		var buf bytes.Buffer
		r := &output.Renderer{Out: &buf, Format: format, IsTTY: true, Quiet: true, Query: ".command"}

		require.NoError(t, r.Render(output.New("status")))

		require.Equal(t, "status\n", buf.String())
	}
}

func TestValidateQuery(t *testing.T) {
	require.NoError(t, output.ValidateQuery(".data.config[].path"))

	err := output.ValidateQuery(".data |")
	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
}
