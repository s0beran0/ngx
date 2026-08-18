package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
)

func TestTestConfigApproved(t *testing.T) {
	f := newFake("local").respond("nginx -t", response{stderr: outputTestOK})

	res, err := New(f).TestConfig(context.Background())
	require.NoError(t, err)

	assert.True(t, res.OK)
	assert.Equal(t, "/etc/nginx/nginx.conf", res.ConfigFile)
	assert.Empty(t, res.Diagnostics)
	assert.NotNil(t, res.Diagnostics)
}

// The Transport invariant applied to the runtime: a rejected configuration is
// a result, not an infrastructure failure. If this becomes an error, the agent
// loses the answer it asked for.
func TestTestConfigRejectedIsNotAnError(t *testing.T) {
	f := newFake("local").respond("nginx -t", response{stderr: outputTestFailed, exit: 1})

	res, err := New(f).TestConfig(context.Background())
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.False(t, res.OK)
	assert.Equal(t, "/etc/nginx/nginx.conf", res.ConfigFile)
	require.Len(t, res.Diagnostics, 1)

	d := res.Diagnostics[0]
	assert.Equal(t, output.SeverityError, d.Severity)
	assert.Equal(t, CodeConfigTest, d.Code)
	assert.Equal(t, `unknown directive "foo"`, d.Message)
	assert.Equal(t, "/etc/nginx/conf.d/a.conf", d.File)
	assert.Equal(t, 3, d.Line)
}

func TestTestConfigWarningDoesNotChangeTheVerdict(t *testing.T) {
	f := newFake("local").respond("nginx -t", response{stderr: outputTestWithWarning})

	res, err := New(f).TestConfig(context.Background())
	require.NoError(t, err)

	assert.True(t, res.OK)
	require.Len(t, res.Diagnostics, 1)
	assert.Equal(t, output.SeverityWarning, res.Diagnostics[0].Severity)
	// Severity never goes into the code: warning and error share the code
	// and are told apart by the severity field.
	assert.Equal(t, CodeConfigTest, res.Diagnostics[0].Code)
}

// The same text, coming from two different transports, produces the same
// result. That is the point of the task: there is no "remote" code path.
func TestTestConfigIdenticalLocalAndRemote(t *testing.T) {
	local := newFake("local").respond("nginx -t", response{stderr: outputTestFailed, exit: 1})
	remote := newFake("ssh://opc@10.0.0.7:22").respond("nginx -t", response{stderr: outputTestFailed, exit: 1})

	a, err := New(local).TestConfig(context.Background())
	require.NoError(t, err)
	b, err := New(remote).TestConfig(context.Background())
	require.NoError(t, err)

	assert.Equal(t, a, b)
}

func TestParseDiagnosticosLocationAtEndOfMessage(t *testing.T) {
	text := `nginx: [emerg] invalid number of arguments in "listen" directive in /etc/nginx/nginx.conf:12`

	diags := ParseDiagnostics(text)
	require.Len(t, diags, 1)
	assert.Equal(t, `invalid number of arguments in "listen" directive`, diags[0].Message)
	assert.Equal(t, "/etc/nginx/nginx.conf", diags[0].File)
	assert.Equal(t, 12, diags[0].Line)
}

func TestParseDiagnosticosWithoutLocation(t *testing.T) {
	diags := ParseDiagnostics(`nginx: [emerg] bind() to 0.0.0.0:80 failed (98: Address already in use)`)
	require.Len(t, diags, 1)
	assert.Empty(t, diags[0].File)
	assert.Zero(t, diags[0].Line)
	assert.Equal(t, output.SeverityError, diags[0].Severity)
}

// An unknown level must not become info: underrating what is not recognized
// hides exactly the new case.
func TestParseDiagnosticsUnknownLevelBecomesError(t *testing.T) {
	diags := ParseDiagnostics("nginx: [xyz] something unexpected")
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityError, diags[0].Severity)
}

func TestParseDiagnosticosListIsNeverNil(t *testing.T) {
	diags := ParseDiagnostics("")
	require.NotNil(t, diags)

	raw, err := json.Marshal(map[string]any{"diagnostics": diags})
	require.NoError(t, err)
	assert.Equal(t, `{"diagnostics":[]}`, string(raw))
}
