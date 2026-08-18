package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/transport"
	"github.com/stretchr/testify/require"
)

// Recorded outputs from a real nginx. No test in this file runs nginx, opens
// a socket or touches the disk: what is at stake is the wiring between the CLI
// and the runtime, and a test that depended on an installed nginx would be
// testing nginx.
const (
	detectOutputOK = `nginx version: nginx/1.24.0
built by gcc 12.2.0 (Debian 12.2.0-14)
built with OpenSSL 3.0.11 19 Sep 2023
TLS SNI support enabled
configure arguments: --prefix=/etc/nginx --conf-path=/etc/nginx/nginx.conf --pid-path=/var/run/nginx.pid --with-http_ssl_module --with-http_geoip_module=dynamic
`

	detectOutputWithoutPIDPath = `nginx version: nginx/1.24.0
configure arguments: --prefix=/etc/nginx --conf-path=/etc/nginx/nginx.conf --with-http_ssl_module
`

	testOutputOK = `nginx: the configuration file /etc/nginx/nginx.conf syntax is ok
nginx: configuration file /etc/nginx/nginx.conf test is successful
`

	testOutputFailed = `nginx: [warn] conflicting server name "app.local" on 0.0.0.0:80, ignored
nginx: [emerg] invalid number of arguments in "listen" directive in /etc/nginx/conf.d/app.conf:12
nginx: configuration file /etc/nginx/nginx.conf test failed
`

	defaultPidfile = "/var/run/nginx.pid"
)

// recordedResponse is what a command wrote, frozen.
type recordedResponse struct {
	stdout string
	stderr string
	exit   int
}

// recordedTransport answers by exact argv and serves files from memory. It is
// richer than the fakeTransport of remote_internal_test.go, which always
// returns the same output: here one command has to answer differently from
// another, because `status` runs two of them.
type recordedTransport struct {
	description string
	responses   map[string]recordedResponse
	files       map[string]string
	openErrors  map[string]error

	mu       sync.Mutex
	executed [][]string
}

func newRecordedTransport() *recordedTransport {
	return &recordedTransport{
		description: "ssh://deploy@10.0.0.9:22",
		responses:   map[string]recordedResponse{},
		files:       map[string]string{},
		openErrors:  map[string]error{},
	}
}

func (t *recordedTransport) respondsTo(argv string, r recordedResponse) *recordedTransport {
	t.responses[argv] = r
	return t
}

func (t *recordedTransport) Open(path string) (io.ReadCloser, error) {
	if err, ok := t.openErrors[path]; ok {
		return nil, err
	}
	if content, ok := t.files[path]; ok {
		return io.NopCloser(strings.NewReader(content)), nil
	}
	return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
}

func (t *recordedTransport) Glob(string) ([]string, error) { return []string{}, nil }

func (t *recordedTransport) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	t.mu.Lock()
	t.executed = append(t.executed, append([]string(nil), argv...))
	t.mu.Unlock()

	r, ok := t.responses[strings.Join(argv, " ")]
	if !ok {
		return nil, []byte("test transport: argv not recorded: " + strings.Join(argv, " ")), 127, nil
	}
	return []byte(r.stdout), []byte(r.stderr), r.exit, nil
}

func (t *recordedTransport) Close() error { return nil }

func (t *recordedTransport) Describe() string { return t.description }

func (t *recordedTransport) calls() [][]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([][]string(nil), t.executed...)
}

// runWithTransport runs the CLI against a recorded transport, entering
// through the remote path (--host). It is the path that proves two things at
// once: that the command runs nginx through the runtime and that it does so on
// the --host target, and not on the machine of whoever typed it.
func runWithTransport(t *testing.T, tr *recordedTransport, args ...string) (output.ExitCode, *bytes.Buffer) {
	t.Helper()

	ctx, out := testContext(t, nil)
	ctx.SSHConfigPath = testSSHConfig(t, "")
	ctx.SSHConnector = func(transport.SSHOptions) (transport.Transport, []output.Diagnostic, error) {
		return tr, nil, nil
	}

	var errBuf bytes.Buffer
	full := append([]string{"--host", "10.0.0.9"}, args...)
	return execute(NewRoot(ctx), ctx, full, &errBuf), out
}

// singleDocument decodes the output and requires it to be a single envelope.
// A second JSON document on stdout would break any consumer.
func singleDocument(t *testing.T, out *bytes.Buffer) output.Envelope {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))

	var env output.Envelope
	require.NoError(t, dec.Decode(&env), "output: %s", out.String())

	var sobra json.RawMessage
	require.ErrorIs(t, dec.Decode(&sobra), io.EOF,
		"stdout has to hold a single envelope, and not two: %s", out.String())

	return env
}

// fields returns the envelope's data as a map, which is the only way to assert
// that a key is *absent* — a destination struct would fill in the zero value
// and the omission would go unnoticed.
func fields(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	var response struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &response), "output: %s", out.String())
	return response.Data
}

func transportWithPassingTest() *recordedTransport {
	return newRecordedTransport().
		respondsTo("nginx -t", recordedResponse{stderr: testOutputOK})
}

func TestTestCommandRunsNginxOnTheTargetAndReturnsTheEnvelope(t *testing.T) {
	tr := transportWithPassingTest()

	code, out := runWithTransport(t, tr, "test")

	require.Equal(t, output.ExitOK, code)
	require.Equal(t, [][]string{{"nginx", "-t"}}, tr.calls())

	env := singleDocument(t, out)
	require.True(t, env.OK)
	require.Equal(t, "test", env.Command)
	require.Equal(t, "ssh://deploy@10.0.0.9:22", env.Meta.Target)
	require.Empty(t, env.Diagnostics)

	data := fields(t, out)
	require.Equal(t, true, data["ok"])
	require.Equal(t, "/etc/nginx/nginx.conf", data["config_file"])
}

// A rejected configuration is a result, not an infrastructure failure: exit 3,
// and the envelope goes out whole, with each diagnostic on the file and line
// nginx reported.
func TestTestCommandWithRejectedConfigIsExit3WithLocatedDiagnostic(t *testing.T) {
	tr := newRecordedTransport().
		respondsTo("nginx -t", recordedResponse{stderr: testOutputFailed, exit: 1})

	code, out := runWithTransport(t, tr, "test")

	require.Equal(t, output.ExitInvalidConfig, code)

	env := singleDocument(t, out)
	require.False(t, env.OK)
	require.Equal(t, "test", env.Command)
	require.Len(t, env.Diagnostics, 2)

	require.Equal(t, output.SeverityWarning, env.Diagnostics[0].Severity)

	emerg := env.Diagnostics[1]
	require.Equal(t, output.SeverityError, emerg.Severity)
	require.Equal(t, "NGX-0224", emerg.Code)
	require.Equal(t, "/etc/nginx/conf.d/app.conf", emerg.File)
	require.Equal(t, 12, emerg.Line)
	require.NotContains(t, emerg.Message, "app.conf:12",
		"the location becomes a field, it does not stay only in the text")

	require.Equal(t, false, fields(t, out)["ok"])
}

// The binary that runs is no longer a detail of the plan: --nginx-bin reaches
// the argv, and --sudo prefixes the command. Without --sudo, no sudo — ngx
// does not escalate privilege on its own (DR5).
func TestTestCommandHonorsSudoAndNginxBin(t *testing.T) {
	t.Run("without --sudo", func(t *testing.T) {
		tr := transportWithPassingTest()
		code, _ := runWithTransport(t, tr, "test")

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, [][]string{{"nginx", "-t"}}, tr.calls())
	})

	t.Run("with --sudo", func(t *testing.T) {
		tr := newRecordedTransport().
			respondsTo("sudo -n nginx -t", recordedResponse{stderr: testOutputOK})

		code, _ := runWithTransport(t, tr, "--sudo", "test")

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, [][]string{{"sudo", "-n", "nginx", "-t"}}, tr.calls())
	})

	t.Run("with --nginx-bin and --sudo", func(t *testing.T) {
		tr := newRecordedTransport().
			respondsTo("sudo -n /usr/local/sbin/nginx -t", recordedResponse{stderr: testOutputOK})

		code, _ := runWithTransport(t, tr,
			"--sudo", "--nginx-bin", "/usr/local/sbin/nginx", "test")

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, [][]string{{"sudo", "-n", "/usr/local/sbin/nginx", "-t"}}, tr.calls())
	})
}

// A missing binary is an infrastructure failure, exit 1 — the opposite of the
// exit 3 of a rejected configuration. The error envelope goes out with the
// runtime's code.
func TestTestCommandWithoutNginxOnTheTargetIsAnInternalFailure(t *testing.T) {
	tr := newRecordedTransport().
		respondsTo("nginx -t", recordedResponse{stderr: "bash: nginx: command not found", exit: 127})

	code, out := runWithTransport(t, tr, "test")

	require.Equal(t, output.ExitInternal, code)

	env := singleDocument(t, out)
	require.False(t, env.OK)
	require.Equal(t, "NGX-0220", env.Diagnostics[0].Code)
}

func transportWithLiveStatus() *recordedTransport {
	tr := newRecordedTransport().
		respondsTo("nginx -V", recordedResponse{stderr: detectOutputOK}).
		respondsTo("kill -0 4242", recordedResponse{})
	tr.files[defaultPidfile] = "4242\n"
	return tr
}

func TestStatusCommandJoinsDetectionAndProcessState(t *testing.T) {
	tr := transportWithLiveStatus()

	code, out := runWithTransport(t, tr, "status")

	require.Equal(t, output.ExitOK, code)

	env := singleDocument(t, out)
	require.True(t, env.OK)
	require.Equal(t, "status", env.Command)
	require.Equal(t, "ssh://deploy@10.0.0.9:22", env.Meta.Target)
	require.Equal(t, "1.24.0", env.Meta.NginxVersion)

	var response struct {
		Data StatusData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &response))

	require.Equal(t, "1.24.0", response.Data.Nginx.Version)
	require.Equal(t, "nginx", response.Data.Nginx.Binary)
	require.Equal(t, "/etc/nginx/nginx.conf", response.Data.Nginx.MainConfig)
	require.Contains(t, response.Data.Nginx.Modules, "http_ssl_module")
	require.Contains(t, response.Data.Nginx.DynamicAvailable, "http_geoip_module")

	require.NotNil(t, response.Data.Process.Running)
	require.True(t, *response.Data.Process.Running)
	require.Equal(t, 4242, response.Data.Process.MasterPID)
	require.Equal(t, defaultPidfile, response.Data.Process.PIDFile)

	// The `kill -0` does not take sudo even when --sudo was asked for on
	// another command: asking whether a pid exists requires no privilege.
	require.Equal(t, [][]string{{"nginx", "-V"}, {"kill", "-0", "4242"}}, tr.calls())
}

// A missing pidfile is evidence, not an assumption: nginx deletes the file
// when it stops. Here running goes out false, with the diagnostic saying why.
func TestStatusCommandWithoutPidfileSaysItIsNotRunning(t *testing.T) {
	tr := newRecordedTransport().
		respondsTo("nginx -V", recordedResponse{stderr: detectOutputOK})

	code, out := runWithTransport(t, tr, "status")

	require.Equal(t, output.ExitOK, code)

	env := singleDocument(t, out)
	require.True(t, env.OK, "a determined state does not bring the command down")
	require.Len(t, env.Diagnostics, 1)
	require.Equal(t, "NGX-0225", env.Diagnostics[0].Code)

	processo := fields(t, out)["process"].(map[string]any)
	require.Equal(t, false, processo["running"])
	require.NotContains(t, processo, "master_pid")
}

// What nginx does not report is omitted, never estimated. A build without
// --pid-path does not say where the pidfile lives, so ngx does not guess a
// path: pid_file goes away, running goes away, and a diagnostic explains the
// absence. Reporting running false here would say nginx went down.
func TestStatusCommandOmitsRunningWhenItCannotTell(t *testing.T) {
	t.Run("build without --pid-path", func(t *testing.T) {
		tr := newRecordedTransport().
			respondsTo("nginx -V", recordedResponse{stderr: detectOutputWithoutPIDPath})

		code, out := runWithTransport(t, tr, "status")

		require.Equal(t, output.ExitOK, code)

		env := singleDocument(t, out)
		require.True(t, env.OK)
		require.Len(t, env.Diagnostics, 1)
		require.Equal(t, output.SeverityWarning, env.Diagnostics[0].Severity)
		require.Equal(t, "NGX-0225", env.Diagnostics[0].Code)

		data := fields(t, out)
		require.NotContains(t, data["nginx"], "pid_path")

		processo := data["process"].(map[string]any)
		require.NotContains(t, processo, "running")
		require.NotContains(t, processo, "pid_file")
	})

	t.Run("pid of another user", func(t *testing.T) {
		tr := newRecordedTransport().
			respondsTo("nginx -V", recordedResponse{stderr: detectOutputOK}).
			respondsTo("kill -0 4242", recordedResponse{
				stderr: "kill: (4242) - Operation not permitted",
				exit:   1,
			})
		tr.files[defaultPidfile] = "4242\n"

		code, out := runWithTransport(t, tr, "status")

		require.Equal(t, output.ExitOK, code)

		env := singleDocument(t, out)
		require.Len(t, env.Diagnostics, 1)
		require.Equal(t, "NGX-0221", env.Diagnostics[0].Code)

		processo := fields(t, out)["process"].(map[string]any)
		require.NotContains(t, processo, "running",
			"with no evidence the field goes away — it never becomes false")
		require.Equal(t, float64(4242), processo["master_pid"])
	})
}

func TestStatusCommandHonorsSudoAndNginxBin(t *testing.T) {
	tr := newRecordedTransport().
		respondsTo("sudo -n /usr/local/sbin/nginx -V", recordedResponse{stderr: detectOutputOK}).
		respondsTo("kill -0 4242", recordedResponse{})
	tr.files[defaultPidfile] = "4242\n"

	code, out := runWithTransport(t, tr,
		"--sudo", "--nginx-bin", "/usr/local/sbin/nginx", "status")

	require.Equal(t, output.ExitOK, code)
	require.Equal(t, [][]string{
		{"sudo", "-n", "/usr/local/sbin/nginx", "-V"},
		{"kill", "-0", "4242"},
	}, tr.calls())

	var response struct {
		Data StatusData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &response))
	require.Equal(t, "/usr/local/sbin/nginx", response.Data.Nginx.Binary)
}

// Missing privilege is reported, never worked around: ngx does not retry the
// command with sudo on its own, and the diagnostic says which command the
// operator would have to authorize.
func TestStatusCommandWithoutPrivilegeReportsAndStopsWithoutRetryingWithSudo(t *testing.T) {
	tr := newRecordedTransport().
		respondsTo("nginx -V", recordedResponse{
			stderr: "nginx: [emerg] open() \"/etc/nginx/nginx.conf\" failed (13: Permission denied)",
			exit:   1,
		})

	code, out := runWithTransport(t, tr, "status")

	require.Equal(t, output.ExitInternal, code)
	require.Equal(t, [][]string{{"nginx", "-V"}}, tr.calls(),
		"the command cannot be retried with sudo on its own")

	env := singleDocument(t, out)
	require.False(t, env.OK)
	require.Equal(t, "NGX-0221", env.Diagnostics[0].Code)
	require.Contains(t, env.Diagnostics[0].Message, "--sudo")
}

// Every runtime diagnostic code the CLI publishes follows section 6.0 of the
// design: NGX- plus four digits, with no letter and no embedded severity.
func TestRuntimeDiagnosticCodesFollowTheSpecFormat(t *testing.T) {
	tr := newRecordedTransport().
		respondsTo("nginx -t", recordedResponse{stderr: testOutputFailed, exit: 1})

	_, out := runWithTransport(t, tr, "test")

	env := singleDocument(t, out)
	require.NotEmpty(t, env.Diagnostics)
	for _, d := range env.Diagnostics {
		require.Regexp(t, `^NGX-\d{4}$`, d.Code)
	}
}

// The human output cannot be the raw JSON when the data knows how to present
// itself.
func TestHumanOutputOfTheRuntimeCommands(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, TestData{OK: true, ConfigFile: "/etc/nginx/nginx.conf"}.RenderHuman(&out))
	require.Equal(t, "configuration accepted: /etc/nginx/nginx.conf\n", out.String())

	running := true
	out.Reset()
	require.NoError(t, StatusData{
		Nginx:   nil,
		Process: ProcessData{Running: &running, MasterPID: 4242},
	}.RenderHuman(&out))
	require.Equal(t, "master 4242 running\n", out.String())

	out.Reset()
	require.NoError(t, StatusData{}.RenderHuman(&out))
	require.Equal(t, "process state unavailable\n", out.String(),
		"an absent field becomes an absent sentence, never a zero")
}
