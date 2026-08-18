//go:build integration

// CLI integration through the remote path, against the bench test bench
// (test/bench).
//
// What is proven here is what only shows up at the end of the line: `inspect
// --host` returning the tree read from the container, the target in the
// envelope's meta, and the redaction of the three secrets before the output
// reaches whoever consumes it. The transport layer has its own tests in
// internal/transport.
//
// Behind the `integration` tag, and it SKIPS when the bench is down.
// Run: make bench-up && go test -tags integration ./... -race
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/transport"
)

const (
	benchHost        = "127.0.0.1"
	benchUser        = "ngxtest"
	defaultBenchPort = 2222
	envBenchPort     = "NGX_BENCH_PORT"

	trapMarker = "LOCAL-TRAP-MUST-NOT-APPEAR"
	trapFile   = "zz-local-trap.conf"

	// The three secrets of the bench, in the three shapes it reproduces.
	// They are the same as in test/bench/generate-config.sh.
	benchToken    = "Bearer ngx-bench-token-4f3c9a1b2e"
	benchHtpasswd = "/etc/nginx/secrets/htpasswd"
	benchTLSKey   = "/etc/nginx/secrets/tls.key"

	remoteTopFile  = "ngx-remote.conf"
	remoteConfDDir = "etc/nginx/conf.d"
	containerFile  = "10-container.conf"
)

// The fixture lives in the HOME of the bench user because /etc/nginx is
// 0700 root:root on purpose (the DR5 trap): as ngxtest, neither `nginx -T` nor
// the SFTP read reaches the real configuration. The relative wildcard is what
// competes with the file of the same name on the local disk.
const remoteTopCLI = `include /usr/share/nginx/modules/*.conf;

events {
    worker_connections 1024;
}

http {
    include ` + remoteConfDDir + `/*.conf;
}
`

const containerConfCLI = `server {
    listen 8080;
    server_name container.bench.local;

    location / {
        auth_basic "area restrita da bench";
        auth_basic_user_file ` + benchHtpasswd + `;

        proxy_set_header Authorization "` + benchToken + `";
        proxy_pass http://127.0.0.1:9000;
    }
}

server {
    listen 8443 ssl;
    server_name tls.bench.local;

    ssl_certificate     /etc/nginx/secrets/tls.crt;
    ssl_certificate_key ` + benchTLSKey + `;

    location / {
        return 200 "tls da bench\n";
    }
}
`

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return root
}

// requireBench skips the test, instead of failing, when the bench is down:
// whoever has no Docker cannot be shown a false failure.
func requireBench(t *testing.T) (key string, port int) {
	t.Helper()

	key = filepath.Join(repoRoot(t), "test", "bench", ".key", "id_ed25519")
	port = defaultBenchPort
	if value := os.Getenv(envBenchPort); value != "" {
		var err error
		port, err = strconv.Atoi(value)
		require.NoErrorf(t, err, "%s=%q is not a port number", envBenchPort, value)
	}

	if _, err := os.Stat(key); err != nil {
		t.Skipf("bench is down: the test key %s does not exist. "+
			"Run `make bench-up` (needs Docker).", key)
	}

	address := net.JoinHostPort(benchHost, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		t.Skipf("bench is down: nothing is listening on %s (%v). "+
			"Run `make bench-up` (needs Docker).", address, err)
	}
	_ = conn.Close()

	// Without the ssh-agent of whoever runs the suite: the bench only
	// accepts the generated key, and an ssh-agent with several keys exhausts
	// the sshd's MaxAuthTries.
	t.Setenv(transport.EnvSocketSSHAgent, "")

	return key, port
}

func benchOptions(key string, port int, knownHosts string) transport.SSHOptions {
	return transport.SSHOptions{
		Host:           benchHost,
		Port:           port,
		User:           benchUser,
		KeyPath:        key,
		KnownHostsPath: knownHosts,
		Timeout:        20 * time.Second,
	}
}

// learnedKnownHosts records the bench's host key by learning it from the
// first-access refusal itself. No test here uses --insecure-host-key: the CLI
// connects with real verification.
func learnedKnownHosts(t *testing.T, key string, port int) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	tr, _, err := transport.SSHWithDiagnostics(benchOptions(key, port, path))
	if err == nil {
		_ = tr.Close()
		t.Fatal("the connection was accepted with an empty known_hosts")
	}

	var e *output.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, transport.CodeUnknownHost, e.Diag.Code)

	// The line is found by its SHAPE, not by cutting on a sentence. Matching
	// prose made this test break the moment the project was translated, and
	// the prose is not the contract anyway -- what matters is that the
	// message hands the operator a line they can paste into known_hosts.
	//
	// Shape of a known_hosts entry: host (bracketed when the port is not 22),
	// key type, base64 key.
	pattern := regexp.MustCompile(`(?m)^.*?(\[?[\w.:-]+\]?(?::\d+)? +ssh-[\w-]+ +[A-Za-z0-9+/=]+)\s*$`)
	matched := pattern.FindStringSubmatch(e.Diag.Message)
	require.NotNilf(t, matched, "the message did not carry a usable known_hosts line: %s", e.Diag.Message)
	line := matched[1]

	require.NoError(t, os.WriteFile(path, []byte(strings.TrimSpace(line)+"\n"), 0o600))
	return path
}

// setupRemoteFixture writes the fixture into the container's HOME and removes
// it at the end. The `sh -c` is here, in the test, and not in ngx: ngx never
// assembles a shell line, and what it runs on the target remains explicit
// argv.
func setupRemoteFixture(t *testing.T, key string, port int, knownHosts string) {
	t.Helper()

	tr, _, err := transport.SSHWithDiagnostics(benchOptions(key, port, knownHosts))
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()

	script := fmt.Sprintf(`set -e
rm -rf "$HOME/etc" "$HOME/%[1]s"
mkdir -p "$HOME/%[2]s"
cat > "$HOME/%[1]s" <<'FIM'
%[3]s
FIM
cat > "$HOME/%[2]s/%[4]s" <<'FIM'
%[5]s
FIM
`, remoteTopFile, remoteConfDDir, remoteTopCLI, containerFile, containerConfCLI)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stdout, stderr, out, err := tr.Run(ctx, []string{"sh", "-c", script})
	require.NoError(t, err)
	require.Zerof(t, out, "setting up the remote fixture failed: %s %s", stdout, stderr)

	t.Cleanup(func() {
		cleanup, _, err := transport.SSHWithDiagnostics(benchOptions(key, port, knownHosts))
		if err != nil {
			return
		}
		defer func() { _ = cleanup.Close() }()
		_, _, _, _ = cleanup.Run(context.Background(),
			[]string{"sh", "-c", fmt.Sprintf(`rm -rf "$HOME/etc" "$HOME/%s"`, remoteTopFile)})
	})
}

// envelopeTree decodes only what this test observes of inspect's data.
type envelopeTree struct {
	Config []struct {
		File string `json:"file"`
	} `json:"config"`
	Summary Summary `json:"summary"`
}

func inspectPayload(t *testing.T, raw []byte) envelopeTree {
	t.Helper()
	var env struct {
		Data envelopeTree `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &env))
	return env.Data
}

// connectionArgs assembles the bench's remote access flags.
func connectionArgs(key string, port int, knownHosts string) []string {
	return []string{
		"--host", benchHost,
		"--port", strconv.Itoa(port),
		"--user", benchUser,
		"--key", key,
		"--known-hosts", knownHosts,
		"--json",
	}
}

// The remote inspect end to end: the tree comes from the container, the target
// shows up in meta, and none of the three secrets crosses the output.
//
// The current directory is the glob trap's (test/bench/local-trap),
// which holds a file with the same name as the container's. If the Glob were
// not injected into the parser, its marker would show up here — and ngx would
// be presenting files from the operator's machine as the server's
// configuration.
func TestRemoteInspectReadsTheContainerAndLeaksNoSecret(t *testing.T) {
	key, port := requireBench(t)
	knownHosts := learnedKnownHosts(t, key, port)
	setupRemoteFixture(t, key, port, knownHosts)

	trap := filepath.Join(repoRoot(t), "test", "bench", "local-trap")
	t.Chdir(trap)

	// Control: on the local disk the same pattern matches the trap. Without
	// this proof, the test would pass vacuously in an empty directory.
	localMatches, err := transport.Local().Glob(remoteConfDDir + "/*.conf")
	require.NoError(t, err)
	require.Contains(t, localMatches, filepath.Join(remoteConfDDir, trapFile),
		"the local trap is not in place")

	ctx, out := testContext(t, nil)
	ctx.SSHConfigPath = testSSHConfig(t, "")

	args := append(connectionArgs(key, port, knownHosts),
		"-c", remoteTopFile, "inspect")

	var errBuf bytes.Buffer
	code := execute(NewRoot(ctx), ctx, args, &errBuf)
	require.Equalf(t, output.ExitOK, code, "stderr: %s\nstdout: %s", errBuf.String(), out.String())

	env := envelopeOf(t, out)
	require.True(t, env.OK)
	// The only expected diagnostic is the informational one about the missing
	// ssh-agent, which the test itself provokes by clearing SSH_AUTH_SOCK.
	for _, d := range env.Diagnostics {
		require.NotEqualf(t, output.SeverityError, d.Severity, "error diagnostic: %s", d.Message)
	}
	require.Equal(t, fmt.Sprintf("ssh://%s@%s:%d", benchUser, benchHost, port), env.Meta.Target)
	require.NotEmpty(t, env.Meta.ConfigHash)

	// The tree is the container's: the file from the relative wildcard, plus
	// the real modules the absolute wildcard brought in.
	data := inspectPayload(t, out.Bytes())
	var paths []string
	modulos := 0
	for _, f := range data.Config {
		paths = append(paths, f.File)
		if strings.HasPrefix(f.File, "/usr/share/nginx/modules/") {
			modulos++
		}
	}
	require.Contains(t, paths, remoteConfDDir+"/"+containerFile)
	require.Positive(t, modulos, "the absolute wildcard brought in no module from the container")
	require.Equal(t, 2, data.Summary.Servers)

	raw := out.String()
	require.NotContains(t, raw, trapMarker,
		"the local disk configuration leaked into the tree read from the bench")
	require.NotContains(t, raw, trapFile)

	// Redaction: none of the three shapes of secret goes out in the output,
	// and the directives stay visible — making the node disappear would make
	// the agent conclude the directive does not exist.
	for _, secret := range []string{benchToken, benchHtpasswd, benchTLSKey} {
		require.NotContainsf(t, raw, secret, "the secret %q leaked into the output", secret)
	}
	require.GreaterOrEqualf(t, strings.Count(raw, output.RedactedValue), 3,
		"the three sensitive directives have to go out redacted, not omitted: %s", raw)
	for _, directive := range []string{"proxy_set_header", "auth_basic_user_file", "ssl_certificate_key"} {
		require.Contains(t, raw, directive)
	}
}

// The other half of DR5, on the reading path: as ngxtest, /etc/nginx is
// unreadable over SFTP. ngx reports the refusal instead of returning an empty
// tree — and does not retry the read with privilege on its own.
func TestRemoteInspectOfTheRealConfigReportsMissingPermission(t *testing.T) {
	key, port := requireBench(t)
	knownHosts := learnedKnownHosts(t, key, port)

	ctx, out := testContext(t, nil)
	ctx.SSHConfigPath = testSSHConfig(t, "")

	args := append(connectionArgs(key, port, knownHosts),
		"-c", "/etc/nginx/nginx.conf", "inspect")

	var errBuf bytes.Buffer
	code := execute(NewRoot(ctx), ctx, args, &errBuf)
	require.NotEqual(t, output.ExitOK, code)

	env := envelopeOf(t, out)
	require.False(t, env.OK)
	require.NotEmpty(t, env.Diagnostics)

	var refusal, diagCode string
	for _, d := range env.Diagnostics {
		if d.Severity == output.SeverityError {
			refusal = strings.ToLower(d.Message)
			diagCode = d.Code
		}
	}
	// The assertion is on the CODE, not on words in the message. The message
	// is prose meant for a human and gets reworded -- and it did: an earlier
	// version of this test matched the Portuguese word "permission" and would
	// have failed the moment the project was translated, in a job nobody runs
	// on every push. The code is the contract a consumer branches on.
	require.Equal(t, "NGX-0003", diagCode,
		"a refused read is invalid configuration, not an internal error")
	require.NotEmpty(t, refusal,
		"the read refusal has to show up; a silently empty tree would be a lie")
	require.NotContains(t, refusal, "permission denied",
		"the raw runtime string cannot leak into the diagnostic")
	require.Nil(t, env.Data, "with no read there is no tree: an unavailable field is omitted")
}
