package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/s0beran0/ngx/internal/output"
)

// No test in this file opens a socket. The ssh.HostKeyCallback is called
// directly, with a hand-built net.Addr — that is how the host key policy is
// tested with no server and no network.

const testHost = "10.0.0.9:22"

// generateKey produces a fresh host key on every call. A test key is generated
// in the test, never committed: a versioned private key is a published key.
func generateKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	return signer.PublicKey()
}

// writeKnownHosts builds a known_hosts in t.TempDir() with the given lines.
func writeKnownHosts(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	content := strings.Join(lines, "\n") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// knownHostsLine formats the key the way known_hosts expects it, for the test
// host.
func knownHostsLine(key ssh.PublicKey) string {
	return knownhosts.Line([]string{knownhosts.Normalize(testHost)}, key)
}

// remoteAddr is the net.Addr the handshake would pass to the callback.
func remoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("10.0.0.9"), Port: 22}
}

// diagnosticOf extracts the Diagnostic from an ngx error. It fails the test if
// the error is not an *output.Error — a bare error has no message of its own
// and serves none of the four outcomes.
func diagnosticOf(t *testing.T, err error) output.Diagnostic {
	t.Helper()
	require.Error(t, err)
	var e *output.Error
	require.ErrorAs(t, err, &e, "the refusal has to carry its own diagnostic")
	return e.Diag
}

// --- Outcome 1: the key checks out ---

func TestHostKeyMatchingKeyProducesNoDiagnostic(t *testing.T) {
	key := generateKey(t)
	path := writeKnownHosts(t, knownHostsLine(key))

	callback, diags, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: path})
	require.NoError(t, err)
	require.NotNil(t, diags, "the list of diagnostics is never nil")
	assert.Empty(t, diags, "the happy path warns about nothing")

	assert.NoError(t, callback(testHost, remoteAddr(), key))
}

// --- Outcome 2: unknown host ---

func TestHostKeyUnknownHostRefusesAndTeachesHowToAdd(t *testing.T) {
	recorded := generateKey(t)
	// The file exists and has entries, just not for this host.
	path := writeKnownHosts(t,
		knownhosts.Line([]string{"outro.exemplo"}, recorded))

	callback, _, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: path})
	require.NoError(t, err)

	presented := generateKey(t)
	diag := diagnosticOf(t, callback(testHost, remoteAddr(), presented))

	assert.Equal(t, CodigoHostDesconhecido, diag.Code)
	assert.Equal(t, output.SeverityError, diag.Severity)
	assert.Contains(t, diag.Message, "10.0.0.9", "the message says which host")
	assert.Contains(t, diag.Message, path, "the message says which file")
	assert.Contains(t, diag.Message, knownHostsLine(presented),
		"the message hands over the ready-made known_hosts line")
	assert.Equal(t, path, diag.File)
}

// --- Outcome 3: the key changed ---

func TestHostKeyChangedKeyReportsPossibleAttack(t *testing.T) {
	recorded := generateKey(t)
	path := writeKnownHosts(t, knownHostsLine(recorded))

	callback, _, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: path})
	require.NoError(t, err)

	presented := generateKey(t)
	diag := diagnosticOf(t, callback(testHost, remoteAddr(), presented))

	assert.Equal(t, CodigoHostKeyAlterada, diag.Code)
	assert.Equal(t, output.SeverityError, diag.Severity)

	lower := strings.ToLower(diag.Message)
	assert.Contains(t, lower, "changed", "the message says the key changed")
	assert.Contains(t, lower, "attack",
		"a changed key is a possible attack and the message has to say so")
	assert.Contains(t, lower, "man-in-the-middle")
	assert.Contains(t, diag.Message, serializeKey(presented),
		"the message shows the presented key")
	assert.Contains(t, diag.Message, serializeKey(recorded),
		"the message shows the recorded key")
	assert.Equal(t, path, diag.File, "points at the file of the diverging record")
	assert.Equal(t, 1, diag.Line, "points at the line of the diverging record")
}

// TestHostKeyUnknownAndChangedAreDistinguishable is the test that justifies
// the task. A test that only checked "it errored" in both cases would pass
// with the same message in both — and whoever reads the output would have no
// way of separating first access from interception, which is the only
// distinction that matters here.
func TestHostKeyUnknownAndChangedAreDistinguishable(t *testing.T) {
	recorded := generateKey(t)
	presented := generateKey(t)

	unknownPath := writeKnownHosts(t,
		knownhosts.Line([]string{"outro.exemplo"}, recorded))
	changedPath := writeKnownHosts(t, knownHostsLine(recorded))

	unknownCallback, _, err := VerificadorHostKey(SSHOptions{KnownHostsPath: unknownPath})
	require.NoError(t, err)
	changedCallback, _, err := VerificadorHostKey(SSHOptions{KnownHostsPath: changedPath})
	require.NoError(t, err)

	unknownDiag := diagnosticOf(t, unknownCallback(testHost, remoteAddr(), presented))
	changedDiag := diagnosticOf(t, changedCallback(testHost, remoteAddr(), presented))

	// 1. Distinguishable by field, which is how an agent separates the
	//    cases without interpreting text.
	assert.NotEqual(t, unknownDiag.Code, changedDiag.Code,
		"the two outcomes need different codes")

	// 2. Distinguishable by text, which is how a human separates them.
	assert.NotEqual(t, unknownDiag.Message, changedDiag.Message)

	// 3. And the difference is the right one: only the changed key case
	//    talks about an attack, and only the unknown host case talks about
	//    a first access.
	assert.NotContains(t, strings.ToLower(unknownDiag.Message), "attack",
		"a first access cannot even mention an attack: whoever filters the output by "+
			"that word has to catch only case 3")
	assert.Contains(t, strings.ToLower(unknownDiag.Message), "first access")
	assert.Contains(t, strings.ToLower(changedDiag.Message), "attack")
	assert.NotContains(t, strings.ToLower(changedDiag.Message), "first access",
		"a swapped key cannot be confused with a first access")

	// 4. And both actually refuse: distinguishing without refusing would not
	//    be DR1.
	assert.Equal(t, output.SeverityError, unknownDiag.Severity)
	assert.Equal(t, output.SeverityError, changedDiag.Severity)
}

// --- Outcome 4a: revoked key ---

func TestHostKeyRevokedKeyHasItsOwnOutcome(t *testing.T) {
	revoked := generateKey(t)
	valid := generateKey(t)
	path := writeKnownHosts(t,
		knownHostsLine(valid),
		"@revoked "+knownHostsLine(revoked))

	callback, _, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: path})
	require.NoError(t, err)

	diag := diagnosticOf(t, callback(testHost, remoteAddr(), revoked))

	assert.Equal(t, CodigoHostKeyRevogada, diag.Code)
	assert.NotEqual(t, CodigoHostDesconhecido, diag.Code)
	assert.NotEqual(t, CodigoHostKeyAlterada, diag.Code)
	assert.Contains(t, strings.ToLower(diag.Message), "revoked")
	assert.Equal(t, path, diag.File, "reports the file of the revocation")
	assert.Equal(t, 2, diag.Line, "reports the line of the revocation")

	// The same policy still accepts the valid key from the same file: the
	// revocation does not contaminate the rest.
	assert.NoError(t, callback(testHost, remoteAddr(), valid))
}

// --- Outcome 4b: missing known_hosts ---

func TestHostKeyMissingKnownHostsHasItsOwnOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")

	callback, diags, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: path})
	require.Error(t, err, "with no file there is nothing to compare; ngx refuses")
	assert.Nil(t, callback)
	assert.Empty(t, diags)

	diag := diagnosticOf(t, err)
	assert.Equal(t, CodigoKnownHostsAusente, diag.Code)
	assert.NotEqual(t, CodigoHostDesconhecido, diag.Code,
		"a missing file is not the same as a host missing from the file")
	assert.Contains(t, diag.Message, path)
	assert.Contains(t, diag.Message, "--insecure-host-key",
		"the message says what the way out is")
	assert.Contains(t, diag.Message, "ssh 10.0.0.9",
		"the message says how to record the host")

	// The original PathError stays reachable for whoever wants to inspect
	// it, but does not leak raw into the message.
	var pathErr *fs.PathError
	assert.True(t, errors.As(err, &pathErr), "the original cause is preserved")
}

func TestHostKeyUnreadableKnownHostsIsNotAMissingFile(t *testing.T) {
	// A directory in place of the file: it exists, but cannot be read.
	path := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.Mkdir(path, 0o700))

	_, _, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: path})
	require.Error(t, err)

	diag := diagnosticOf(t, err)
	assert.NotEqual(t, CodigoKnownHostsAusente, diag.Code,
		"an unreadable file has a different cause and a different fix than a missing one")
	assert.Contains(t, diag.Message, path)
}

// --- The DR1 escape hatch ---

func TestHostKeyInsecureAcceptsAnyKeyButWarns(t *testing.T) {
	callback, diags, err := VerificadorHostKey(SSHOptions{
		Host:            "10.0.0.9",
		InsecureHostKey: true,
	})
	require.NoError(t, err)
	require.NotNil(t, callback)

	// Accepts any key, including one never seen before.
	assert.NoError(t, callback(testHost, remoteAddr(), generateKey(t)))
	assert.NoError(t, callback(testHost, remoteAddr(), generateKey(t)))

	// But never in silence.
	require.Len(t, diags, 1, "using the escape hatch has to show up in the output")
	assert.Equal(t, output.SeverityWarning, diags[0].Severity)
	assert.Equal(t, CodigoAvisoHostKeyInsegura, diags[0].Code)
	assert.Contains(t, diags[0].Message, "--insecure-host-key")
	assert.Contains(t, diags[0].Message, "10.0.0.9")
	assert.Contains(t, strings.ToLower(diags[0].Message), "man-in-the-middle",
		"the warning says what was lost, not just that a flag was used")
}

func TestHostKeyInsecureIgnoresMissingKnownHosts(t *testing.T) {
	// The escape hatch serves precisely whoever does not have the host
	// recorded: it cannot trip over the missing file before taking effect.
	callback, diags, err := VerificadorHostKey(SSHOptions{
		Host:            "10.0.0.9",
		KnownHostsPath:  filepath.Join(t.TempDir(), "nao-existe"),
		InsecureHostKey: true,
	})
	require.NoError(t, err)
	require.Len(t, diags, 1)
	assert.NoError(t, callback(testHost, remoteAddr(), generateKey(t)))
}

// --- Contract shared by the refusal outcomes ---

func TestHostKeyRefusalsUseDocumentedExitCode(t *testing.T) {
	recorded := generateKey(t)
	presented := generateKey(t)

	withOtherKey := writeKnownHosts(t, knownHostsLine(recorded))
	withoutHost := writeKnownHosts(t, knownhosts.Line([]string{"outro.exemplo"}, recorded))
	withRevoked := writeKnownHosts(t, "@revoked "+knownHostsLine(presented))

	refuse := func(t *testing.T, path string) error {
		t.Helper()
		callback, _, err := VerificadorHostKey(SSHOptions{KnownHostsPath: path})
		require.NoError(t, err)
		return callback(testHost, remoteAddr(), presented)
	}

	cases := map[string]error{
		"host desconhecido": refuse(t, withoutHost),
		"chave alterada":    refuse(t, withOtherKey),
		"chave revogada":    refuse(t, withRevoked),
	}

	seen := map[string]string{}
	for name, err := range cases {
		// v0.1 only documents the exit codes it emits; a host key refusal
		// falls under 1 (internal/IO) until there is a documented code of
		// its own.
		assert.Equal(t, output.ExitInternal, output.CodeOf(err), name)

		diag := diagnosticOf(t, err)
		previous, repeated := seen[diag.Code]
		assert.False(t, repeated, "%s and %s share the code %s", name, previous, diag.Code)
		seen[diag.Code] = name
	}

	// Adding the missing known_hosts, that is four distinct codes.
	_, _, err := VerificadorHostKey(SSHOptions{
		KnownHostsPath: filepath.Join(t.TempDir(), "nao-existe"),
	})
	seen[diagnosticOf(t, err).Code] = "known_hosts ausente"
	assert.Len(t, seen, 4, "the four refusal outcomes are distinguishable by code")
}
