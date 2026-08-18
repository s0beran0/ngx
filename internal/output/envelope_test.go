package output_test

import (
	"encoding/json"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// An agent consuming the output does `.diagnostics.length`. A null list
// breaks that access, so an empty list needs to serialize as [].
func TestEnvelopeSerializesEmptyDiagnosticsAsArray(t *testing.T) {
	env := output.New("status")

	b, err := json.Marshal(env)
	require.NoError(t, err)

	require.Contains(t, string(b), `"diagnostics":[]`)
	require.NotContains(t, string(b), `"diagnostics":null`)
}

func TestEnvelopeStartsOK(t *testing.T) {
	env := output.New("status")

	require.True(t, env.OK)
	require.Equal(t, "status", env.Command)
	require.Equal(t, output.Version, env.NgxVersion)
}

// Golden test: locks the JSON tags of the envelope, the diagnostic and the
// meta against future renaming. Later tasks consume those names verbatim;
// without this test, any field could be renamed without breaking anything
// here.
func TestEnvelopeSerializesAllFieldsWithExpectedTags(t *testing.T) {
	env := output.New("lint")
	env.Data = map[string]string{"result": "ok"}
	env.Meta = output.Meta{
		DurationMS:   42,
		NginxVersion: "1.25.3",
		ConfigHash:   "abc123",
	}
	env.AddDiagnostic(output.Diagnostic{
		Severity: output.SeverityError,
		Code:     "NGX-0002",
		Message:  "malformed selector",
		File:     "nginx.conf",
		Line:     10,
		Column:   4,
		Selector: "http.server[0]",
		ID:       "diag-1",
		Docs:     "https://example.com/NGX-0002",
	})

	b, err := json.Marshal(env)
	require.NoError(t, err)

	expected := `{
		"ok": false,
		"command": "lint",
		"ngx_version": "0.1.0-dev",
		"data": {"result": "ok"},
		"diagnostics": [
			{
				"severity": "error",
				"code": "NGX-0002",
				"message": "malformed selector",
				"file": "nginx.conf",
				"line": 10,
				"column": 4,
				"selector": "http.server[0]",
				"id": "diag-1",
				"docs": "https://example.com/NGX-0002"
			}
		],
		"meta": {
			"duration_ms": 42,
			"nginx_version": "1.25.3",
			"config_hash": "abc123"
		}
	}`

	require.JSONEq(t, expected, string(b))
}

// The error severity is what brings the envelope's ok down. Warning and info
// do not.
func TestAddDiagnosticErrorBringsOKDown(t *testing.T) {
	env := output.New("test")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityWarning, Message: "careful"})
	require.True(t, env.OK, "warning must not bring ok down")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityInfo, Message: "just a note"})
	require.True(t, env.OK, "info must not bring ok down")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityError, Message: "failed"})
	require.False(t, env.OK, "error must bring ok down")

	require.Len(t, env.Diagnostics, 3)
}

// Absent optional fields must not pollute the agent's output.
func TestDiagnosticOmitsEmptyFields(t *testing.T) {
	b, err := json.Marshal(output.Diagnostic{
		Severity: output.SeverityError,
		Code:     "NGX-0002",
		Message:  "malformed selector",
	})
	require.NoError(t, err)

	s := string(b)
	require.NotContains(t, s, `"file"`, "an empty file must be omitted")
	require.NotContains(t, s, `"line"`, "a zero line must be omitted")
	require.NotContains(t, s, `"column"`, "a zero column must be omitted")
	require.NotContains(t, s, `"selector"`, "an empty selector must be omitted")
	require.NotContains(t, s, `"id"`, "an empty id must be omitted")
	require.NotContains(t, s, `"docs"`, "an empty docs must be omitted")
}
