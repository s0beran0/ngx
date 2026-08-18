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

const hostDeTeste = "10.0.0.9:22"

// gerarChave produces a fresh host key on every call. A test key is generated
// in the test, never committed: a versioned private key is a published key.
func gerarChave(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	return signer.PublicKey()
}

// escreverKnownHosts builds a known_hosts in t.TempDir() with the given lines.
func escreverKnownHosts(t *testing.T, linhas ...string) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "known_hosts")
	conteudo := strings.Join(linhas, "\n") + "\n"
	require.NoError(t, os.WriteFile(caminho, []byte(conteudo), 0o600))
	return caminho
}

// linhaKnownHosts formats the key the way known_hosts expects it, for the test
// host.
func linhaKnownHosts(key ssh.PublicKey) string {
	return knownhosts.Line([]string{knownhosts.Normalize(hostDeTeste)}, key)
}

// enderecoRemoto is the net.Addr the handshake would pass to the callback.
func enderecoRemoto() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("10.0.0.9"), Port: 22}
}

// diagnosticoDe extracts the Diagnostic from an ngx error. It fails the test if
// the error is not an *output.Error — a bare error has no message of its own
// and serves none of the four outcomes.
func diagnosticoDe(t *testing.T, err error) output.Diagnostic {
	t.Helper()
	require.Error(t, err)
	var e *output.Error
	require.ErrorAs(t, err, &e, "the refusal has to carry its own diagnostic")
	return e.Diag
}

// --- Outcome 1: the key checks out ---

func TestHostKeyChaveConfereNaoProduzDiagnostico(t *testing.T) {
	key := gerarChave(t)
	caminho := escreverKnownHosts(t, linhaKnownHosts(key))

	callback, diags, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: caminho})
	require.NoError(t, err)
	require.NotNil(t, diags, "the list of diagnostics is never nil")
	assert.Empty(t, diags, "the happy path warns about nothing")

	assert.NoError(t, callback(hostDeTeste, enderecoRemoto(), key))
}

// --- Outcome 2: unknown host ---

func TestHostKeyHostDesconhecidoRecusaEEnsinaAAdicionar(t *testing.T) {
	registrada := gerarChave(t)
	// The file exists and has entries, just not for this host.
	caminho := escreverKnownHosts(t,
		knownhosts.Line([]string{"outro.exemplo"}, registrada))

	callback, _, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: caminho})
	require.NoError(t, err)

	apresentada := gerarChave(t)
	diag := diagnosticoDe(t, callback(hostDeTeste, enderecoRemoto(), apresentada))

	assert.Equal(t, CodigoHostDesconhecido, diag.Code)
	assert.Equal(t, output.SeverityError, diag.Severity)
	assert.Contains(t, diag.Message, "10.0.0.9", "the message says which host")
	assert.Contains(t, diag.Message, caminho, "the message says which file")
	assert.Contains(t, diag.Message, linhaKnownHosts(apresentada),
		"the message hands over the ready-made known_hosts line")
	assert.Equal(t, caminho, diag.File)
}

// --- Outcome 3: the key changed ---

func TestHostKeyChaveAlteradaAcusaPossivelAtaque(t *testing.T) {
	registrada := gerarChave(t)
	caminho := escreverKnownHosts(t, linhaKnownHosts(registrada))

	callback, _, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: caminho})
	require.NoError(t, err)

	apresentada := gerarChave(t)
	diag := diagnosticoDe(t, callback(hostDeTeste, enderecoRemoto(), apresentada))

	assert.Equal(t, CodigoHostKeyAlterada, diag.Code)
	assert.Equal(t, output.SeverityError, diag.Severity)

	minuscula := strings.ToLower(diag.Message)
	assert.Contains(t, minuscula, "changed", "the message says the key changed")
	assert.Contains(t, minuscula, "attack",
		"a changed key is a possible attack and the message has to say so")
	assert.Contains(t, minuscula, "man-in-the-middle")
	assert.Contains(t, diag.Message, serializarChave(apresentada),
		"the message shows the presented key")
	assert.Contains(t, diag.Message, serializarChave(registrada),
		"the message shows the recorded key")
	assert.Equal(t, caminho, diag.File, "points at the file of the diverging record")
	assert.Equal(t, 1, diag.Line, "points at the line of the diverging record")
}

// TestHostKeyDesconhecidoEAlteradaSaoDistinguiveis is the test that justifies
// the task. A test that only checked "it errored" in both cases would pass
// with the same message in both — and whoever reads the output would have no
// way of separating first access from interception, which is the only
// distinction that matters here.
func TestHostKeyDesconhecidoEAlteradaSaoDistinguiveis(t *testing.T) {
	registrada := gerarChave(t)
	apresentada := gerarChave(t)

	caminhoDesconhecido := escreverKnownHosts(t,
		knownhosts.Line([]string{"outro.exemplo"}, registrada))
	caminhoAlterada := escreverKnownHosts(t, linhaKnownHosts(registrada))

	callbackDesconhecido, _, err := VerificadorHostKey(SSHOptions{KnownHostsPath: caminhoDesconhecido})
	require.NoError(t, err)
	callbackAlterada, _, err := VerificadorHostKey(SSHOptions{KnownHostsPath: caminhoAlterada})
	require.NoError(t, err)

	diagDesconhecido := diagnosticoDe(t, callbackDesconhecido(hostDeTeste, enderecoRemoto(), apresentada))
	diagAlterada := diagnosticoDe(t, callbackAlterada(hostDeTeste, enderecoRemoto(), apresentada))

	// 1. Distinguishable by field, which is how an agent separates the
	//    cases without interpreting text.
	assert.NotEqual(t, diagDesconhecido.Code, diagAlterada.Code,
		"the two outcomes need different codes")

	// 2. Distinguishable by text, which is how a human separates them.
	assert.NotEqual(t, diagDesconhecido.Message, diagAlterada.Message)

	// 3. And the difference is the right one: only the changed key case
	//    talks about an attack, and only the unknown host case talks about
	//    a first access.
	assert.NotContains(t, strings.ToLower(diagDesconhecido.Message), "attack",
		"a first access cannot even mention an attack: whoever filters the output by "+
			"that word has to catch only case 3")
	assert.Contains(t, strings.ToLower(diagDesconhecido.Message), "first access")
	assert.Contains(t, strings.ToLower(diagAlterada.Message), "attack")
	assert.NotContains(t, strings.ToLower(diagAlterada.Message), "first access",
		"a swapped key cannot be confused with a first access")

	// 4. And both actually refuse: distinguishing without refusing would not
	//    be DR1.
	assert.Equal(t, output.SeverityError, diagDesconhecido.Severity)
	assert.Equal(t, output.SeverityError, diagAlterada.Severity)
}

// --- Outcome 4a: revoked key ---

func TestHostKeyChaveRevogadaTemDesfechoProprio(t *testing.T) {
	revogada := gerarChave(t)
	valida := gerarChave(t)
	caminho := escreverKnownHosts(t,
		linhaKnownHosts(valida),
		"@revoked "+linhaKnownHosts(revogada))

	callback, _, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: caminho})
	require.NoError(t, err)

	diag := diagnosticoDe(t, callback(hostDeTeste, enderecoRemoto(), revogada))

	assert.Equal(t, CodigoHostKeyRevogada, diag.Code)
	assert.NotEqual(t, CodigoHostDesconhecido, diag.Code)
	assert.NotEqual(t, CodigoHostKeyAlterada, diag.Code)
	assert.Contains(t, strings.ToLower(diag.Message), "revoked")
	assert.Equal(t, caminho, diag.File, "reports the file of the revocation")
	assert.Equal(t, 2, diag.Line, "reports the line of the revocation")

	// The same policy still accepts the valid key from the same file: the
	// revocation does not contaminate the rest.
	assert.NoError(t, callback(hostDeTeste, enderecoRemoto(), valida))
}

// --- Outcome 4b: missing known_hosts ---

func TestHostKeyKnownHostsAusenteTemDesfechoProprio(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "known_hosts")

	callback, diags, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: caminho})
	require.Error(t, err, "with no file there is nothing to compare; ngx refuses")
	assert.Nil(t, callback)
	assert.Empty(t, diags)

	diag := diagnosticoDe(t, err)
	assert.Equal(t, CodigoKnownHostsAusente, diag.Code)
	assert.NotEqual(t, CodigoHostDesconhecido, diag.Code,
		"a missing file is not the same as a host missing from the file")
	assert.Contains(t, diag.Message, caminho)
	assert.Contains(t, diag.Message, "--insecure-host-key",
		"the message says what the way out is")
	assert.Contains(t, diag.Message, "ssh 10.0.0.9",
		"the message says how to record the host")

	// The original PathError stays reachable for whoever wants to inspect
	// it, but does not leak raw into the message.
	var pathErr *fs.PathError
	assert.True(t, errors.As(err, &pathErr), "the original cause is preserved")
}

func TestHostKeyKnownHostsIlegivelNaoViraArquivoAusente(t *testing.T) {
	// A directory in place of the file: it exists, but cannot be read.
	caminho := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.Mkdir(caminho, 0o700))

	_, _, err := VerificadorHostKey(SSHOptions{Host: "10.0.0.9", KnownHostsPath: caminho})
	require.Error(t, err)

	diag := diagnosticoDe(t, err)
	assert.NotEqual(t, CodigoKnownHostsAusente, diag.Code,
		"an unreadable file has a different cause and a different fix than a missing one")
	assert.Contains(t, diag.Message, caminho)
}

// --- The DR1 escape hatch ---

func TestHostKeyInseguroAceitaQualquerChaveMasAvisa(t *testing.T) {
	callback, diags, err := VerificadorHostKey(SSHOptions{
		Host:            "10.0.0.9",
		InsecureHostKey: true,
	})
	require.NoError(t, err)
	require.NotNil(t, callback)

	// Accepts any key, including one never seen before.
	assert.NoError(t, callback(hostDeTeste, enderecoRemoto(), gerarChave(t)))
	assert.NoError(t, callback(hostDeTeste, enderecoRemoto(), gerarChave(t)))

	// But never in silence.
	require.Len(t, diags, 1, "using the escape hatch has to show up in the output")
	assert.Equal(t, output.SeverityWarning, diags[0].Severity)
	assert.Equal(t, CodigoAvisoHostKeyInsegura, diags[0].Code)
	assert.Contains(t, diags[0].Message, "--insecure-host-key")
	assert.Contains(t, diags[0].Message, "10.0.0.9")
	assert.Contains(t, strings.ToLower(diags[0].Message), "man-in-the-middle",
		"the warning says what was lost, not just that a flag was used")
}

func TestHostKeyInseguroIgnoraKnownHostsAusente(t *testing.T) {
	// The escape hatch serves precisely whoever does not have the host
	// recorded: it cannot trip over the missing file before taking effect.
	callback, diags, err := VerificadorHostKey(SSHOptions{
		Host:            "10.0.0.9",
		KnownHostsPath:  filepath.Join(t.TempDir(), "nao-existe"),
		InsecureHostKey: true,
	})
	require.NoError(t, err)
	require.Len(t, diags, 1)
	assert.NoError(t, callback(hostDeTeste, enderecoRemoto(), gerarChave(t)))
}

// --- Contract shared by the refusal outcomes ---

func TestHostKeyRecusasUsamExitCodeDocumentado(t *testing.T) {
	registrada := gerarChave(t)
	apresentada := gerarChave(t)

	comChaveOutra := escreverKnownHosts(t, linhaKnownHosts(registrada))
	semOHost := escreverKnownHosts(t, knownhosts.Line([]string{"outro.exemplo"}, registrada))
	comRevogada := escreverKnownHosts(t, "@revoked "+linhaKnownHosts(apresentada))

	recusa := func(t *testing.T, caminho string) error {
		t.Helper()
		callback, _, err := VerificadorHostKey(SSHOptions{KnownHostsPath: caminho})
		require.NoError(t, err)
		return callback(hostDeTeste, enderecoRemoto(), apresentada)
	}

	casos := map[string]error{
		"host desconhecido": recusa(t, semOHost),
		"chave alterada":    recusa(t, comChaveOutra),
		"chave revogada":    recusa(t, comRevogada),
	}

	vistos := map[string]string{}
	for nome, err := range casos {
		// v0.1 only documents the exit codes it emits; a host key refusal
		// falls under 1 (internal/IO) until there is a documented code of
		// its own.
		assert.Equal(t, output.ExitInternal, output.CodeOf(err), nome)

		diag := diagnosticoDe(t, err)
		anterior, repetido := vistos[diag.Code]
		assert.False(t, repetido, "%s and %s share the code %s", nome, anterior, diag.Code)
		vistos[diag.Code] = nome
	}

	// Adding the missing known_hosts, that is four distinct codes.
	_, _, err := VerificadorHostKey(SSHOptions{
		KnownHostsPath: filepath.Join(t.TempDir(), "nao-existe"),
	})
	vistos[diagnosticoDe(t, err).Code] = "known_hosts ausente"
	assert.Len(t, vistos, 4, "the four refusal outcomes are distinguishable by code")
}
