package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/transport"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// fakeTransport records what the CLI asked of the transport without
// touching any socket at all.
type fakeTransport struct {
	description string
	closed      int
	argv        [][]string
	stdout      string
}

func (t *fakeTransport) Open(string) (io.ReadCloser, error) { return nil, errors.New("unused") }
func (t *fakeTransport) Glob(string) ([]string, error)      { return []string{}, nil }

func (t *fakeTransport) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	t.argv = append(t.argv, argv)
	return []byte(t.stdout), nil, 0, nil
}

func (t *fakeTransport) Close() error { t.closed++; return nil }

func (t *fakeTransport) Describe() string {
	if t.description == "" {
		return "ssh://deploy@10.0.0.9:22"
	}
	return t.description
}

// fakeConnector replaces transport.SSHWithDiagnostics and keeps the options
// the resolution produced, which is what the precedence tests observe.
type fakeConnector struct {
	calls int
	opts  transport.SSHOptions
	tr    *fakeTransport
	diags []output.Diagnostic
	err   error
}

func (c *fakeConnector) connector(opts transport.SSHOptions) (transport.Transport, []output.Diagnostic, error) {
	c.calls++
	c.opts = opts
	if c.err != nil {
		return nil, c.diags, c.err
	}
	if c.tr == nil {
		c.tr = &fakeTransport{}
	}
	return c.tr, c.diags, nil
}

// testContext assembles a Context isolated from the real filesystem and
// from the HOME of whoever runs the suite.
func testContext(t *testing.T, con *fakeConnector) (*Context, *bytes.Buffer) {
	t.Helper()
	global, local := isolatedPaths(t)
	var out bytes.Buffer
	ctx := &Context{
		Flags:              &GlobalFlags{},
		Renderer:           &output.Renderer{Out: &out, IsTTY: false},
		GlobalSettingsPath: global,
		LocalSettingsPath:  local,
	}
	if con != nil {
		ctx.SSHConnector = con.connector
	}
	return ctx, &out
}

func envelopeOf(t *testing.T, out *bytes.Buffer) output.Envelope {
	t.Helper()
	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	return env
}

// testSSHConfig writes a fixture ~/.ssh/config and returns the path.
func testSSHConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// Without --host the behavior is exactly today's: local transport, no
// connection built, and not even ~/.ssh/config gets read. All of v0.1 is local
// use; a regression here breaks what already works.
//
// SSHConfigPath points at a deliberately invalid file: if the remote
// resolution ran, it would produce a DR7 warning in the envelope. The absence
// of any diagnostic is the proof that nobody read it.
func TestWithoutHostTheTransportIsLocalAndNoSSHIsBuilt(t *testing.T) {
	con := &fakeConnector{}
	ctx, out := testContext(t, con)
	ctx.SSHConfigPath = testSSHConfig(t, "Match user deploy\n  Port not-a-number\n")

	var errBuf bytes.Buffer
	code := execute(NewRoot(ctx), ctx, []string{"version"}, &errBuf)

	require.Equal(t, output.ExitOK, code)
	require.Zero(t, con.calls, "no SSH connection may be built without --host")

	env := envelopeOf(t, out)
	require.True(t, env.OK)
	require.Equal(t, "local", env.Meta.Target)
	require.Empty(t, env.Diagnostics)
}

// The non-regression proof on the production entry point itself: without
// --host, Execute assembles the local transport and inspect reads the fixture
// from disk through the same os.Open/filepath.Glob as always.
func TestWithoutHostInspectKeepsReadingTheLocalDisk(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := Execute(
		[]string{"-c", filepath.Join("testdata", "example.conf"), "inspect"},
		&out, &errBuf, false,
	)

	require.Equal(t, output.ExitOK, code)

	env := envelopeOf(t, &out)
	require.True(t, env.OK)
	require.Equal(t, "local", env.Meta.Target)
	require.Empty(t, env.Diagnostics)
	require.NotEmpty(t, env.Meta.ConfigHash)
}

// DR2's precedence: an explicit flag beats ~/.ssh/config, which beats the
// default. The one doing that is transport.ResolveSSHConfig; the test proves
// the CLI feeds it the right flags and does not reimplement the order on the
// side.
func TestFlagsTakePrecedenceOverSSHConfig(t *testing.T) {
	const file = "Host web1\n" +
		"  HostName 10.0.0.9\n" +
		"  User deploy\n" +
		"  Port 2222\n" +
		"  IdentityFile /keys/id_web1\n"

	t.Run("the flag beats the file", func(t *testing.T) {
		con := &fakeConnector{}
		ctx, _ := testContext(t, con)
		ctx.SSHConfigPath = testSSHConfig(t, file)

		var errBuf bytes.Buffer
		code := execute(NewRoot(ctx), ctx,
			[]string{"--host", "web1", "--port", "2200", "--user", "root", "version"}, &errBuf)

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, 1, con.calls)
		require.Equal(t, "10.0.0.9", con.opts.Host)
		require.Equal(t, 2200, con.opts.Port)
		require.Equal(t, "root", con.opts.User)
		require.Equal(t, "/keys/id_web1", con.opts.KeyPath)
	})

	t.Run("the file beats the default", func(t *testing.T) {
		con := &fakeConnector{}
		ctx, _ := testContext(t, con)
		ctx.SSHConfigPath = testSSHConfig(t, file)

		var errBuf bytes.Buffer
		code := execute(NewRoot(ctx), ctx, []string{"--host", "web1", "version"}, &errBuf)

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, 2222, con.opts.Port)
		require.Equal(t, "deploy", con.opts.User)
	})

	t.Run("the default when nobody says", func(t *testing.T) {
		con := &fakeConnector{}
		ctx, _ := testContext(t, con)
		ctx.SSHConfigPath = testSSHConfig(t, "")

		var errBuf bytes.Buffer
		code := execute(NewRoot(ctx), ctx, []string{"--host", "10.0.0.9", "version"}, &errBuf)

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, transport.DefaultSSHPort, con.opts.Port)
		require.NotEmpty(t, con.opts.User)
	})
}

// The global --timeout applies to the connection: without it, an unreachable
// host would hang on the transport's internal timeout, and not on the one the
// operator asked for.
func TestGlobalTimeoutReachesTheConnectionOptions(t *testing.T) {
	con := &fakeConnector{}
	ctx, _ := testContext(t, con)
	ctx.SSHConfigPath = testSSHConfig(t, "")

	var errBuf bytes.Buffer
	code := execute(NewRoot(ctx), ctx, []string{"--host", "h", "--timeout", "5s", "version"}, &errBuf)

	require.Equal(t, output.ExitOK, code)
	require.Equal(t, "5s", con.opts.Timeout.String())
}

// No secret crosses the command line: --password does not exist, and cobra
// refuses the unknown flag with a usage exit.
func TestPasswordFlagDoesNotExist(t *testing.T) {
	con := &fakeConnector{}
	ctx, out := testContext(t, con)

	var errBuf bytes.Buffer
	code := execute(NewRoot(ctx), ctx, []string{"--host", "h", "--password", "s3cr3t", "version"}, &errBuf)

	require.Equal(t, output.ExitUsage, code)
	require.Zero(t, con.calls)

	env := envelopeOf(t, out)
	require.False(t, env.OK)
	require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
}

// The password is never assembled by the CLI either: the Password field
// leaves the options empty, so that transport.BuildAuthentication fetches it
// from NGX_SSH_PASSWORD or from the prompt with no echo.
func TestTheCLINeverFillsThePasswordInTheOptions(t *testing.T) {
	con := &fakeConnector{}
	ctx, _ := testContext(t, con)
	ctx.SSHConfigPath = testSSHConfig(t, "")

	var errBuf bytes.Buffer
	code := execute(NewRoot(ctx), ctx, []string{"--host", "h", "version"}, &errBuf)

	require.Equal(t, output.ExitOK, code)
	require.Empty(t, con.opts.Password)
}

// DR1's escape hatch is never silent: --insecure-host-key reaches the
// transport and the warning it returns shows up in the envelope, without
// bringing ok down.
func TestInsecureHostKeyWarningReachesTheEnvelope(t *testing.T) {
	con := &fakeConnector{
		diags: []output.Diagnostic{{
			Severity: output.SeverityWarning,
			Code:     transport.CodeInsecureHostKeyWarning,
			Message:  "host key accepted without verification",
		}},
		tr: &fakeTransport{description: "ssh://deploy@10.0.0.9:22"},
	}
	ctx, out := testContext(t, con)
	ctx.SSHConfigPath = testSSHConfig(t, "")

	var errBuf bytes.Buffer
	code := execute(NewRoot(ctx), ctx,
		[]string{"--host", "10.0.0.9", "--insecure-host-key", "version"}, &errBuf)

	require.Equal(t, output.ExitOK, code)
	require.True(t, con.opts.InsecureHostKey)

	env := envelopeOf(t, out)
	require.True(t, env.OK, "a warning does not bring the command down")
	require.Len(t, env.Diagnostics, 1)
	require.Equal(t, output.SeverityWarning, env.Diagnostics[0].Severity)
	require.Equal(t, transport.CodeInsecureHostKeyWarning, env.Diagnostics[0].Code)
	require.Equal(t, "ssh://deploy@10.0.0.9:22", env.Meta.Target)
}

// A connection diagnostic also has to survive the error path: whoever reads
// the failure needs to know the host key was not verified.
func TestConnectionDiagnosticSurvivesTheCommandError(t *testing.T) {
	con := &fakeConnector{
		diags: []output.Diagnostic{{
			Severity: output.SeverityWarning,
			Code:     transport.CodeInsecureHostKeyWarning,
			Message:  "host key accepted without verification",
		}},
	}
	ctx, out := testContext(t, con)
	ctx.SSHConfigPath = testSSHConfig(t, "")

	root := NewRoot(ctx)
	root.AddCommand(&cobra.Command{
		Use:  "fail",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return output.InvalidConfig("invalid configuration")
		},
	})

	var errBuf bytes.Buffer
	code := execute(root, ctx, []string{"--host", "h", "--insecure-host-key", "fail"}, &errBuf)

	require.Equal(t, output.ExitInvalidConfig, code)

	env := envelopeOf(t, out)
	require.False(t, env.OK)
	codigos := make([]string, 0, len(env.Diagnostics))
	for _, d := range env.Diagnostics {
		codigos = append(codigos, d.Code)
	}
	require.Contains(t, codigos, transport.CodeInsecureHostKeyWarning)
	require.Contains(t, codigos, "NGX-0003")
}

// A connection error keeps the transport's code in the envelope, and meta does
// not invent a target: with no transport built, the field is omitted.
func TestConnectionFailurePreservesTheTransportDiagnostic(t *testing.T) {
	con := &fakeConnector{err: &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     transport.CodeUnknownHost,
			Message:  "unknown host",
		},
	}}
	ctx, out := testContext(t, con)
	ctx.SSHConfigPath = testSSHConfig(t, "")

	var errBuf bytes.Buffer
	code := execute(NewRoot(ctx), ctx, []string{"--host", "h", "version"}, &errBuf)

	require.Equal(t, output.ExitInternal, code)

	env := envelopeOf(t, out)
	require.False(t, env.OK)
	require.Equal(t, transport.CodeUnknownHost, env.Diagnostics[0].Code)
	require.Empty(t, env.Meta.Target, "an unconfirmed target cannot be estimated")
}

// Close always runs — on success and on the command's error.
func TestTransportClosesOnBothPaths(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		tr := &fakeTransport{}
		con := &fakeConnector{tr: tr}
		ctx, _ := testContext(t, con)
		ctx.SSHConfigPath = testSSHConfig(t, "")

		var errBuf bytes.Buffer
		require.Equal(t, output.ExitOK,
			execute(NewRoot(ctx), ctx, []string{"--host", "h", "version"}, &errBuf))
		require.Equal(t, 1, tr.closed)
	})

	t.Run("command error", func(t *testing.T) {
		tr := &fakeTransport{}
		con := &fakeConnector{tr: tr}
		ctx, _ := testContext(t, con)
		ctx.SSHConfigPath = testSSHConfig(t, "")

		root := NewRoot(ctx)
		root.AddCommand(&cobra.Command{
			Use:  "fail",
			Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				return output.InvalidConfig("invalid configuration")
			},
		})

		var errBuf bytes.Buffer
		require.Equal(t, output.ExitInvalidConfig,
			execute(root, ctx, []string{"--host", "h", "fail"}, &errBuf))
		require.Equal(t, 1, tr.closed)
	})
}

// A connection flag without --host is a usage error, not a value silently
// ignored.
func TestConnectionFlagWithoutHostIsUsageError(t *testing.T) {
	for _, args := range [][]string{
		{"--user", "deploy", "version"},
		{"--port", "2222", "version"},
		{"--key", "/keys/id", "version"},
		{"--insecure-host-key", "version"},
		{"--known-hosts", "/tmp/kh", "version"},
	} {
		t.Run(args[0], func(t *testing.T) {
			con := &fakeConnector{}
			ctx, out := testContext(t, con)

			var errBuf bytes.Buffer
			code := execute(NewRoot(ctx), ctx, args, &errBuf)

			require.Equal(t, output.ExitUsage, code)
			require.Zero(t, con.calls)

			env := envelopeOf(t, out)
			require.False(t, env.OK)
			require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
			require.Contains(t, env.Diagnostics[0].Message, "--host")
		})
	}
}

// --sudo is local too: explicit privilege applies to both targets, so the flag
// does not require --host.
func TestSudoDoesNotRequireAHost(t *testing.T) {
	con := &fakeConnector{}
	ctx, _ := testContext(t, con)

	var errBuf bytes.Buffer
	require.Equal(t, output.ExitOK,
		execute(NewRoot(ctx), ctx, []string{"--sudo", "version"}, &errBuf))
	require.Zero(t, con.calls)
	require.True(t, ctx.Flags.Sudo)
}

// DR5 in the wiring: with --sudo, the runtime assembled by the context
// escalates; without it, never. What the transport receives is the proof.
func TestSudoReachesTheRuntime(t *testing.T) {
	cases := []struct {
		name  string
		sudo  bool
		first string
	}{
		{"with --sudo", true, "sudo"},
		{"without --sudo", false, "nginx"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := &fakeTransport{stdout: "configuration file /etc/nginx/nginx.conf test is successful\n"}
			ctx := &Context{Flags: &GlobalFlags{Sudo: c.sudo}, Transport: tr}

			_, err := ctx.NewRuntime().TestConfig(context.Background())
			require.NoError(t, err)

			require.Len(t, tr.argv, 1)
			require.Equal(t, c.first, tr.argv[0][0])
		})
	}
}

// The binary configured by --nginx-bin is part of the wiring too.
func TestNginxBinReachesTheRuntime(t *testing.T) {
	tr := &fakeTransport{stdout: "test is successful\n"}
	ctx := &Context{Flags: &GlobalFlags{NginxBin: "/opt/nginx/sbin/nginx"}, Transport: tr}

	_, err := ctx.NewRuntime().TestConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "/opt/nginx/sbin/nginx", tr.argv[0][0])
}

// Every diagnostic code produced by this path follows section 6.0 of the
// spec: NGX- plus four digits, with no letter and no embedded severity.
func TestDiagnosticCodesFollowTheFormat(t *testing.T) {
	con := &fakeConnector{
		diags: []output.Diagnostic{{
			Severity: output.SeverityWarning,
			Code:     transport.CodeSSHConfigWarning,
			Message:  "warning",
		}},
	}
	ctx, out := testContext(t, con)
	ctx.SSHConfigPath = testSSHConfig(t, "")

	var errBuf bytes.Buffer
	require.Equal(t, output.ExitOK,
		execute(NewRoot(ctx), ctx, []string{"--host", "h", "version"}, &errBuf))

	env := envelopeOf(t, out)
	require.NotEmpty(t, env.Diagnostics)
	for _, d := range env.Diagnostics {
		require.Regexp(t, `^NGX-\d{4}$`, d.Code)
	}
}

// The warning about an unreachable ~/.ssh/config uses DR7's code and does not
// abort the connection: the resolution goes on with flags and defaults.
func TestUnavailableSSHConfigPathBecomesAWarningNotAnError(t *testing.T) {
	ctx, _ := testContext(t, nil)
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	path, diag := sshConfigPath(ctx)
	if diag == nil {
		// On platforms where the user's directory is discovered by other
		// means there is nothing to warn about — but then the path has to
		// exist.
		require.NotEmpty(t, path)
		return
	}
	require.Empty(t, path)
	require.Equal(t, output.SeverityWarning, diag.Severity)
	require.Equal(t, transport.CodeSSHConfigWarning, diag.Code)
	require.True(t, strings.HasPrefix(diag.Code, "NGX-"))
}
