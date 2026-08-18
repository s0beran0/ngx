package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
)

func TestDumpSplitsFiles(t *testing.T) {
	f := newFake("local").respond("nginx -T", response{stdout: outputDump, stderr: outputTestOK})

	d, err := New(f).DumpConfig(context.Background())
	require.NoError(t, err)

	assert.True(t, d.OK)
	assert.Equal(t, "/etc/nginx/nginx.conf", d.ConfigFile)
	require.Len(t, d.Files, 2)

	assert.Equal(t, "/etc/nginx/nginx.conf", d.Files[0].Path)
	assert.Contains(t, d.Files[0].Content, "worker_processes auto;")
	assert.NotContains(t, d.Files[0].Content, "# configuration file")

	assert.Equal(t, "/etc/nginx/conf.d/site.conf", d.Files[1].Path)
	assert.Contains(t, d.Files[1].Content, "server_name example.com;")
	// The last file does not get one more blank line than the others.
	assert.True(t, strings.HasSuffix(d.Files[1].Content, "}\n"))
}

func TestDumpIdenticalLocalAndRemote(t *testing.T) {
	local := newFake("local").respond("nginx -T", response{stdout: outputDump, stderr: outputTestOK})
	remote := newFake("ssh://opc@10.0.0.7:22").respond("nginx -T", response{stdout: outputDump, stderr: outputTestOK})

	a, err := New(local).DumpConfig(context.Background())
	require.NoError(t, err)
	b, err := New(remote).DumpConfig(context.Background())
	require.NoError(t, err)

	assert.Equal(t, a, b)
}

// An invalid configuration makes `-T` exit non-zero and dump nothing. It is
// still a result.
func TestDumpInvalidConfigIsNotAnError(t *testing.T) {
	f := newFake("local").respond("nginx -T", response{stderr: outputTestFailed, exit: 1})

	d, err := New(f).DumpConfig(context.Background())
	require.NoError(t, err)

	assert.False(t, d.OK)
	assert.Empty(t, d.Files)
	require.Len(t, d.Diagnostics, 1)
	assert.Equal(t, output.SeverityError, d.Diagnostics[0].Severity)

	raw, err := json.Marshal(d)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"files":[]`)
}

// DR5, the case measured on the real host: `nginx -T` fails for an ordinary
// user. Without --sudo ngx reports the requirement and says what the command
// is -- and does not try again.
func TestDumpWithoutPrivilegeReportsAndDoesNotEscalate(t *testing.T) {
	f := newFake("ssh://opc@10.0.0.7:22").respond("nginx -T", response{
		stderr: outputNoPrivilege,
		exit:   1,
	})

	_, err := New(f).DumpConfig(context.Background())

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodePrivilegeRequired, e.Diag.Code)
	assert.Equal(t, output.SeverityError, e.Diag.Severity)
	assert.Contains(t, e.Diag.Message, "--sudo")
	assert.Contains(t, e.Diag.Message, "sudo -n nginx -T")

	// The point of DR5: a single call, with no sudo. Escalating in silence
	// is the defect the decision exists to prevent.
	calls := f.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, []string{"nginx", "-T"}, calls[0])
}

// The other path: with an explicit --sudo, the command runs escalated and
// returns the configuration.
func TestDumpWithSudoRunsEscalated(t *testing.T) {
	f := newFake("ssh://opc@10.0.0.7:22").respond("sudo -n nginx -T", response{
		stdout: outputDump,
		stderr: outputTestOK,
	})

	d, err := New(f, WithSudo(true)).DumpConfig(context.Background())
	require.NoError(t, err)
	assert.True(t, d.OK)
	assert.Len(t, d.Files, 2)

	calls := f.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, []string{"sudo", "-n", "nginx", "-T"}, calls[0])
}

// Same recording, same files: the result with --sudo and the result without
// any privilege requirement must not differ in anything beyond how they were
// obtained.
func TestDumpWithSudoEqualsWithoutSudo(t *testing.T) {
	withoutSudo := newFake("local").respond("nginx -T", response{stdout: outputDump, stderr: outputTestOK})
	withSudo := newFake("local").respond("sudo -n nginx -T", response{stdout: outputDump, stderr: outputTestOK})

	a, err := New(withoutSudo).DumpConfig(context.Background())
	require.NoError(t, err)
	b, err := New(withSudo, WithSudo(true)).DumpConfig(context.Background())
	require.NoError(t, err)

	assert.Equal(t, a, b)
}

// --sudo requested on a target whose sudo wants a password: ngx has no TTY
// and nowhere to send the password, so this is an outcome of its own, not a
// generic "needs privilege".
func TestDumpSudoRequiresPassword(t *testing.T) {
	f := newFake("ssh://opc@10.0.0.7:22").respond("sudo -n nginx -T", response{
		stderr: "sudo: a password is required\n",
		exit:   1,
	})

	_, err := New(f, WithSudo(true)).DumpConfig(context.Background())

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodeSudoUnavailable, e.Diag.Code)
}

// With --sudo and still no permission, the message must not tell the user to
// use --sudo again.
func TestDumpWithSudoStillWithoutPermission(t *testing.T) {
	f := newFake("local").respond("sudo -n nginx -T", response{
		stderr: outputNoPrivilege,
		exit:   1,
	})

	_, err := New(f, WithSudo(true)).DumpConfig(context.Background())

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodePrivilegeRequired, e.Diag.Code)
	assert.Contains(t, e.Diag.Message, "with --sudo")
}

// A syntax error must not be confused with a missing privilege: the detection
// is conservative on purpose.
func TestTestConfigSyntaxErrorDoesNotBecomePrivilege(t *testing.T) {
	f := newFake("local").respond("nginx -t", response{stderr: outputTestFailed, exit: 1})

	res, err := New(f).TestConfig(context.Background())
	require.NoError(t, err)
	assert.False(t, res.OK)
}

func TestSplitDumpIgnoresContentBeforeFirstMarker(t *testing.T) {
	files := SplitDump("loose junk\n# configuration file /a.conf:\nfoo;\n")
	require.Len(t, files, 1)
	assert.Equal(t, "/a.conf", files[0].Path)
	assert.Equal(t, "foo;\n", files[0].Content)
}

// A comment inside a configuration must not split the file in two: the marker
// only counts at the start of the line and with the trailing colon.
func TestSplitDumpDoesNotConfuseComment(t *testing.T) {
	text := "# configuration file /a.conf:\n    # configuration file /fake.conf:\nfoo;\n"
	files := SplitDump(text)
	require.Len(t, files, 1)
	assert.Contains(t, files[0].Content, "/fake.conf")
}

func TestSplitDumpEmpty(t *testing.T) {
	files := SplitDump("")
	require.NotNil(t, files)
	assert.Empty(t, files)
}
