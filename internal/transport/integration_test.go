//go:build integration

// Integration tests of the remote path against the test bench (test/bancada):
// a throwaway container with sshd and nginx that reproduces the shape measured
// on a production nginx — three wildcard patterns, 130 files in the effective
// configuration, /etc/nginx readable by root only and a secret inside the
// configuration.
//
// They sit behind the `integration` tag because they need Docker: `go test
// ./...` without the tag touches no container at all. With the tag and no
// bench running, the tests SKIP with the instructions to bring it up — whoever
// cloned the project and has no Docker must not see a false failure.
//
// Run: make bancada-up && go test -tags integration ./... -race
//
// The package is transport_test, and not transport, because the explicit
// privilege test (DR5) uses internal/runtime, which imports
// internal/transport: from inside the package that would be an import cycle.
package transport_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/runtime"
	"github.com/s0beran0/ngx/internal/transport"
)

const (
	benchHost = "127.0.0.1"
	benchUser = "ngxtest"
	// The port is fixed in the Makefile so the test knows where to connect;
	// whoever brought the bench up with another BANCADA_PORTA says so
	// through the variable.
	defaultBenchPort = 2222
	envBenchPort     = "NGX_BANCADA_PORTA"

	// The effective configuration of the container has 130 files, checked
	// during the image build itself. The tolerance matches the one in
	// smoke.sh: the number of dynamic modules changes if an nginx-mod-*
	// package comes in or goes out, and what the test proves is the order
	// of magnitude, not the inventory.
	benchFileCount = 130
	fileTolerance  = 5

	// marcadorArmadilha exists only in the same-named file on the LOCAL
	// disk (test/bancada/armadilha-local). Seeing it means the file from
	// the local disk was read instead of the one in the container.
	marcadorArmadilha = "ARMADILHA-LOCAL-NAO-DEVE-APARECER"
	arquivoArmadilha  = "zz-armadilha-local.conf"

	// The three secrets of the bench, in the three shapes it reproduces.
	benchToken    = "Bearer ngx-bancada-token-4f3c9a1b2e"
	benchHtpasswd = "/etc/nginx/secrets/htpasswd"
	benchTLSKey   = "/etc/nginx/secrets/tls.key"

	// The remote fixture, written into the HOME of the bench user.
	remoteTopFile  = "ngx-remoto.conf"
	remoteConfDDir = "etc/nginx/conf.d"
	containerFile  = "10-do-container.conf"
	confDPattern   = remoteConfDDir + "/*.conf"
	modulesPattern = "/usr/share/nginx/modules/*.conf"
)

// ---------------------------------------------------------------------------
// Bancada
// ---------------------------------------------------------------------------

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return root
}

func benchPort(t *testing.T) int {
	t.Helper()
	value := os.Getenv(envBenchPort)
	if value == "" {
		return defaultBenchPort
	}
	port, err := strconv.Atoi(value)
	require.NoErrorf(t, err, "%s=%q is not a port number", envBenchPort, value)
	return port
}

// requireBench skips the test, instead of failing it, when the bench is not
// running: with no Docker the remote path cannot be exercised, and a failure
// there would say nothing about ngx.
func requireBench(t *testing.T) (key string, port int) {
	t.Helper()

	key = filepath.Join(repoRoot(t), "test", "bancada", ".chave", "id_ed25519")
	port = benchPort(t)

	if _, err := os.Stat(key); err != nil {
		t.Skipf("bench not running: the test key %s does not exist. "+
			"Run `make bancada-up` (needs Docker).", key)
	}

	address := net.JoinHostPort(benchHost, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		t.Skipf("bench not running: nothing listens on %s (%v). "+
			"Run `make bancada-up` (needs Docker).", address, err)
	}
	_ = conn.Close()

	// The ssh-agent of whoever runs the suite is left out: the bench only
	// accepts the generated key, and an agent holding several keys exhausts
	// the sshd MaxAuthTries before reaching it. With no agent the method
	// simply drops out of the list — and the path this test wants to
	// exercise is the key one.
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

// knownHostsLinePrefix is what the unknown-host message writes right
// before the line that is ready for known_hosts.
const knownHostsLinePrefix = "append the line to the file: "

func knownHostsLine(t *testing.T, err error) string {
	t.Helper()
	var e *output.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, transport.CodeUnknownHost, e.Diag.Code)

	_, line, found := strings.Cut(e.Diag.Message, knownHostsLinePrefix)
	require.Truef(t, found,
		"the unknown-host message did not carry the known_hosts line: %s", e.Diag.Message)
	return strings.TrimSpace(line)
}

// learnedKnownHosts records the bench host key in a temporary known_hosts,
// learning it from the first-access refusal itself.
//
// No test here uses --insecure-host-key: they all connect with real host key
// verification, which is how ngx runs in production.
func learnedKnownHosts(t *testing.T, key string, port int) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	tr, _, err := transport.SSHWithDiagnostics(benchOptions(key, port, path))
	if err == nil {
		_ = tr.Close()
		t.Fatal("the connection was accepted with an empty known_hosts: an unknown host has to be refused")
	}

	require.NoError(t, os.WriteFile(path, []byte(knownHostsLine(t, err)+"\n"), 0o600))
	return path
}

func connectToBench(t *testing.T) transport.Transport {
	t.Helper()

	key, port := requireBench(t)
	tr, diags, err := transport.SSHWithDiagnostics(
		benchOptions(key, port, learnedKnownHosts(t, key, port)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tr.Close() })

	for _, d := range diags {
		require.NotEqualf(t, transport.CodeInsecureHostKeyWarning, d.Code,
			"the test has to connect with host key verification: %s", d.Message)
	}
	return tr
}

// ---------------------------------------------------------------------------
// Fixture remota
// ---------------------------------------------------------------------------

// The fixture lives in the HOME of the bench user because /etc/nginx is
// 0700 root:root on purpose — the DR5 trap — and neither `nginx -T` nor an
// SFTP read reaches the real configuration as ngxtest. The files below repeat
// the shapes that matter: a relative wildcard (which resolves against the
// directory of the top file, and is therefore the one the local-disk trap
// competes for), an absolute wildcard over real container files, and the three
// secret shapes of the bench.
const remoteTop = `# Fixture of the ngx integration test.

# Absolute wildcard: four real container files, from the nginx-mod-* packages.
# They do not exist on the disk of whoever runs the test.
include ` + modulesPattern + `;

events {
    worker_connections 1024;
}

http {
    # Relative wildcard, resolved against the directory of the top file (the
    # remote HOME). On the local disk, the same pattern hits the trap.
    include ` + confDPattern + `;
}
`

const containerConf = `# MARCADOR-DO-CONTAINER
#
# The three secret shapes are the same as in the real bench configuration
# (test/bancada/gerar-config.sh).
server {
    listen 8080;
    server_name do-container.bancada.local;

    location / {
        auth_basic "area restrita da bancada";
        auth_basic_user_file ` + benchHtpasswd + `;

        proxy_set_header Authorization "` + benchToken + `";
        proxy_pass http://127.0.0.1:9000;
    }
}

server {
    listen 8443 ssl;
    server_name tls.bancada.local;

    ssl_certificate     /etc/nginx/secrets/tls.crt;
    ssl_certificate_key ` + benchTLSKey + `;

    location / {
        return 200 "tls da bancada\n";
    }
}
`

// setupRemoteFixture writes the fixture into the container HOME and removes
// it at the end. The `sh -c` is here, in the test, and not in ngx: ngx never
// assembles a shell line, and what it runs on the target is still explicit
// argv.
func setupRemoteFixture(t *testing.T, tr transport.Transport) {
	t.Helper()

	script := fmt.Sprintf(`set -e
rm -rf "$HOME/etc" "$HOME/%[1]s"
mkdir -p "$HOME/%[2]s"
cat > "$HOME/%[1]s" <<'FIM'
%[3]s
FIM
cat > "$HOME/%[2]s/%[4]s" <<'FIM'
%[5]s
FIM
`, remoteTopFile, remoteConfDDir, remoteTop, containerFile, containerConf)

	run(t, tr, script)
	t.Cleanup(func() {
		_, _, _, _ = tr.Run(context.Background(),
			[]string{"sh", "-c", fmt.Sprintf(`rm -rf "$HOME/etc" "$HOME/%s"`, remoteTopFile)})
	})
}

func run(t *testing.T, tr transport.Transport, script string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stdout, stderr, exitCode, err := tr.Run(ctx, []string{"sh", "-c", script})
	require.NoError(t, err)
	require.Zerof(t, exitCode, "setting up the remote fixture failed: %s %s", stdout, stderr)
}

func treePaths(t *config.Tree) []string {
	paths := make([]string, 0, len(t.Files))
	for _, f := range t.Files {
		paths = append(paths, f.Path)
	}
	return paths
}

// ---------------------------------------------------------------------------
// 1. The glob resolves the CONTAINER files
// ---------------------------------------------------------------------------

// This is the test that keeps the defect Task R3 fixed from coming back: with
// Glob not injected, crossplane resolved `include conf.d/*.conf` through
// filepath.Glob over the disk of whoever ran ngx, and presented the operator's
// own machine files as the server configuration (DR4).
//
// The trap is a same-named file on the local disk. The test changes the
// current directory to it and proves, in the same run, both halves: that the
// pattern does match the trap locally (otherwise the test would pass
// vacuously), and that the tree read from the bench has the container file and
// no trace of the trap.
func TestGlobResolvesContainerFilesNotLocalDiskFiles(t *testing.T) {
	armadilha := filepath.Join(repoRoot(t), "test", "bancada", "armadilha-local")

	tr := connectToBench(t)
	setupRemoteFixture(t, tr)

	t.Chdir(armadilha)

	localMatches, err := transport.Local().Glob(confDPattern)
	require.NoError(t, err)
	require.Containsf(t, localMatches, filepath.Join(remoteConfDDir, arquivoArmadilha),
		"the local trap is not in place: %s should match %s from %s",
		confDPattern, arquivoArmadilha, armadilha)

	tree, err := config.Parse(config.ParseOptions{
		Path: remoteTopFile,
		Open: tr.Open,
		Glob: tr.Glob,
	})
	require.NoError(t, err)

	paths := treePaths(tree)
	require.Contains(t, paths, remoteConfDDir+"/"+containerFile,
		"the relative wildcard did not bring in the container file")

	modules := 0
	for _, f := range tree.Files {
		require.NotContainsf(t, f.Path, "armadilha",
			"a file from the local disk got into the tree read from the bench: %s", f.Path)
		require.NotContainsf(t, string(f.Source), marcadorArmadilha,
			"the local trap marker leaked into the tree, coming from %s", f.Path)
		if strings.HasPrefix(f.Path, "/usr/share/nginx/modules/") {
			modules++
		}
	}
	require.Positive(t, modules, "the absolute wildcard brought in no container module")
	require.Contains(t, paths, remoteTopFile)
}

// ---------------------------------------------------------------------------
// 2. The effective configuration of the container, with its ~130 files
// ---------------------------------------------------------------------------

// The 130 files are only reachable through `nginx -T` with privilege:
// /etc/nginx is 0700 root:root, so the SFTP read (the inspect path) stops at
// the first file. This test is therefore the other half of the pair with the
// privilege one below: with --sudo the dump exists and it is the container's.
func TestRemoteDumpWithSudoReturnsContainerEffectiveConfig(t *testing.T) {
	tr := connectToBench(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dump, err := runtime.New(tr, runtime.WithSudo(true)).DumpConfig(ctx)
	require.NoError(t, err)
	require.True(t, dump.OK)
	require.Contains(t, dump.ConfigFile, "/etc/nginx/nginx.conf")
	require.InDeltaf(t, benchFileCount, len(dump.Files), fileTolerance,
		"the effective configuration of the container has %d files; the dump brought %d",
		benchFileCount, len(dump.Files))

	// The three bench wildcards resolved inside the container.
	byPrefix := map[string]int{
		"/etc/nginx/conf.d/":        0,
		"/etc/nginx/default.d/":     0,
		"/usr/share/nginx/modules/": 0,
	}
	for _, f := range dump.Files {
		for prefix := range byPrefix {
			if strings.HasPrefix(f.Path, prefix) {
				byPrefix[prefix]++
			}
		}
		require.NotContainsf(t, f.Content, marcadorArmadilha,
			"the local trap marker showed up in the container dump, in %s", f.Path)
	}
	for prefix, n := range byPrefix {
		require.Positivef(t, n, "no file came from %s", prefix)
	}
	require.Greater(t, byPrefix["/etc/nginx/conf.d/"], 100,
		"conf.d is the big directory of the bench")
}

// ---------------------------------------------------------------------------
// 3. An unknown host is refused BEFORE it enters known_hosts
// ---------------------------------------------------------------------------

// DR1 demands two different messages: a first access is normal friction, a
// changed key is a possible attack. Mixing them up is the dangerous defect —
// whoever reads "the key changed" on a first access learns to ignore the
// warning that will one day matter for real.
func TestUnknownHostIsRefusedBeforeEnteringKnownHosts(t *testing.T) {
	key, port := requireBench(t)

	path := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	tr, _, firstAccessErr := transport.SSHWithDiagnostics(benchOptions(key, port, path))
	if firstAccessErr == nil {
		_ = tr.Close()
		t.Fatal("the connection was accepted with an empty known_hosts")
	}

	var e *output.Error
	require.ErrorAs(t, firstAccessErr, &e)
	require.Equal(t, transport.CodeUnknownHost, e.Diag.Code)
	require.NotEqual(t, transport.CodeHostKeyChanged, e.Diag.Code)

	msg := e.Diag.Message
	require.Contains(t, msg, "unknown host")
	require.Contains(t, msg, "first access")
	require.NotContains(t, msg, "CHANGED")
	require.NotContains(t, msg, "attack")
	require.Equal(t, path, e.Diag.File)

	// ngx does not learn the key on its own: the operator is who records it.
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Empty(t, content, "known_hosts was written by ngx")

	// And with the key recorded the same connection goes through — the
	// refusal came from the verification, not from the credential.
	line := knownHostsLine(t, firstAccessErr)
	require.NoError(t, os.WriteFile(path, []byte(line+"\n"), 0o600))

	tr, diags, err := transport.SSHWithDiagnostics(benchOptions(key, port, path))
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()

	require.Equal(t, fmt.Sprintf("ssh://%s@%s:%d", benchUser, benchHost, port), tr.Describe())
	for _, d := range diags {
		require.NotEqual(t, transport.CodeInsecureHostKeyWarning, d.Code)
	}
}

// ---------------------------------------------------------------------------
// 4. Explicit privilege (DR5)
// ---------------------------------------------------------------------------

// The bench was built with this trap: `nginx -T` fails for the ordinary user
// and passwordless sudo does exist, restricted to the nginx binary. The path
// that "just works" would be to escalate silently; ngx reports and stops.
func TestWithoutSudoNgxReportsPrivilegeRequirementAndDoesNotEscalate(t *testing.T) {
	tr := connectToBench(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dump, err := runtime.New(tr).DumpConfig(ctx)
	require.Nil(t, dump, "with no privilege there is no dump: an unavailable field is omitted")
	require.Error(t, err)

	var e *output.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, runtime.CodePrivilegeRequired, e.Diag.Code)

	msg := e.Diag.Message
	require.Contains(t, msg, "`nginx -T`", "the command that ran carried no sudo")
	require.Contains(t, msg, "--sudo")
	require.Contains(t, msg, "sudo -n nginx -T", "the message has to say what the privileged command is")
	require.NotContains(t, msg, benchToken)

	// The same call, with --sudo, works: the bench allows passwordless sudo
	// for nginx. That is, the refusal above was a decision by ngx, not a
	// missing path.
	dump, err = runtime.New(tr, runtime.WithSudo(true)).DumpConfig(ctx)
	require.NoError(t, err)
	require.True(t, dump.OK)
	require.NotEmpty(t, dump.Files)
}

// ---------------------------------------------------------------------------
// 5. Redaction: the three bench secrets do not leak
// ---------------------------------------------------------------------------

// What is proved here is that the secret IS in the configuration read from the
// container — which is what makes the CLI redaction test (internal/cli) a real
// proof, and not a proof over an empty configuration.
func TestTheThreeSecretsAreInTheConfigReadFromTheBench(t *testing.T) {
	tr := connectToBench(t)
	setupRemoteFixture(t, tr)

	tree, err := config.Parse(config.ParseOptions{
		Path: remoteTopFile,
		Open: tr.Open,
		Glob: tr.Glob,
	})
	require.NoError(t, err)

	var text strings.Builder
	for _, f := range tree.Files {
		text.Write(f.Source)
	}
	for _, secret := range []string{benchToken, benchHtpasswd, benchTLSKey} {
		require.Containsf(t, text.String(), secret,
			"the configuration read from the bench does not have the secret %q", secret)
	}
}
