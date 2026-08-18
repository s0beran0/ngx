package transport

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
)

// escreverConfig writes a test ~/.ssh/config and returns its path.
func escreverConfig(t *testing.T, conteudo string) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(caminho, []byte(conteudo), 0o600))
	return caminho
}

// usuarioEsperado works out the current user on its own, without calling the
// function under test: comparing the implementation against itself would prove
// nothing.
func usuarioEsperado(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	require.NoError(t, err)
	nome := u.Username
	if i := strings.LastIndex(nome, `\`); i >= 0 {
		nome = nome[i+1:]
	}
	require.NotEmpty(t, nome)
	return nome
}

func TestResolverLeDiretivasDoArquivo(t *testing.T) {
	caminho := escreverConfig(t, `
Host web1
  HostName 10.0.0.1
  User deploy
  Port 2222
  IdentityFile /keys/web1_ed25519
`)

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)

	require.NoError(t, err)
	assert.Empty(t, diags)
	assert.Equal(t, "10.0.0.1", opts.Host, "HostName maps the alias to the real target")
	assert.Equal(t, "deploy", opts.User)
	assert.Equal(t, 2222, opts.Port)
	assert.Equal(t, "/keys/web1_ed25519", opts.KeyPath)
}

func TestResolverCasaWildcardEmHost(t *testing.T) {
	caminho := escreverConfig(t, `
Host web*
  User deploy
  Port 2222
`)

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web42"}, caminho)

	require.NoError(t, err)
	assert.Empty(t, diags)
	assert.Equal(t, "web42", opts.Host, "with no HostName the target stays the alias")
	assert.Equal(t, "deploy", opts.User)
	assert.Equal(t, 2222, opts.Port)
}

func TestResolverHostAusenteDoArquivoUsaDefaults(t *testing.T) {
	caminho := escreverConfig(t, `
Host web1
  User deploy
  Port 2222
`)

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "db1"}, caminho)

	require.NoError(t, err)
	assert.Empty(t, diags, "a host missing from the file is not an anomaly")
	assert.Equal(t, "db1", opts.Host)
	assert.Equal(t, PortaSSHPadrao, opts.Port)
	assert.Equal(t, usuarioEsperado(t), opts.User)
}

func TestResolverArquivoAusenteUsaDefaultsSemAviso(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "nao-existe", "config")

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)

	require.NoError(t, err)
	assert.Empty(t, diags, "whoever has no ~/.ssh/config does not deserve a warning")
	assert.Equal(t, 22, opts.Port)
	assert.Equal(t, usuarioEsperado(t), opts.User)
}

// TestResolverPrecedenciaDR2 proves the three levels in a single file: the flag
// beats the file, the file beats the default, and the default covers whatever
// nobody stated.
func TestResolverPrecedenciaDR2(t *testing.T) {
	caminho := escreverConfig(t, `
Host web1
  User deploy
  Port 2222
`)

	// Flag beats file: User and Port come from the flag.
	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1", User: "root", Port: 22022}, caminho)
	require.NoError(t, err)
	assert.Empty(t, diags)
	assert.Equal(t, "root", opts.User)
	assert.Equal(t, 22022, opts.Port)

	// File beats default: with no flag, the file's values hold.
	opts, _, err = ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)
	require.NoError(t, err)
	assert.Equal(t, "deploy", opts.User)
	assert.Equal(t, 2222, opts.Port)

	// The default covers the rest: a host the file does not mention.
	opts, _, err = ResolverSSHConfig(SSHOptions{Host: "outro"}, caminho)
	require.NoError(t, err)
	assert.Equal(t, usuarioEsperado(t), opts.User)
	assert.Equal(t, PortaSSHPadrao, opts.Port)
}

// TestResolverFlagVaziaNaoSobrescreveArquivo locks down the classic precedence
// bug: treating "" and 0 as a user choice erases what the file says and sends
// the connection to the wrong place.
func TestResolverFlagVaziaNaoSobrescreveArquivo(t *testing.T) {
	caminho := escreverConfig(t, `
Host web1
  HostName 10.0.0.1
  User deploy
  Port 2222
  IdentityFile /keys/web1_ed25519
`)

	flags := SSHOptions{Host: "web1", User: "", Port: 0, KeyPath: ""}
	opts, diags, err := ResolverSSHConfig(flags, caminho)

	require.NoError(t, err)
	assert.Empty(t, diags)
	assert.Equal(t, "deploy", opts.User)
	assert.Equal(t, 2222, opts.Port)
	assert.Equal(t, "/keys/web1_ed25519", opts.KeyPath)
}

// TestResolverMatchNaoSuportadoDegradaComAviso is DR7. A `Match user` is valid
// for ssh and makes kevinburke/ssh_config fail the WHOLE file — including the
// Host blocks it would have understood. ngx has to resolve anyway and say what
// it lost, with file and line.
func TestResolverMatchNaoSuportadoDegradaComAviso(t *testing.T) {
	caminho := escreverConfig(t,
		"Host web1\n"+ // line 1
			"  HostName 10.0.0.1\n"+ // line 2
			"  User deploy\n"+ // line 3
			"  Port 2222\n"+ // line 4
			"\n"+ // line 5
			"Match user deploy\n"+ // line 6
			"  IdentityFile /keys/deploy\n") // line 7

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1", User: "root"}, caminho)

	// Side 1: it resolves anyway, from the flag and the defaults. Nothing
	// from the file gets in — not even the Host web1 block, which on its
	// own would be readable.
	require.NoError(t, err, "an unreadable file never aborts")
	assert.Equal(t, "web1", opts.Host, "the HostName from the file was not read")
	assert.Equal(t, "root", opts.User, "the explicit flag still holds")
	assert.Equal(t, PortaSSHPadrao, opts.Port, "the Port from the file was not read")

	// Side 2: the warning comes out, and says where. A resolver that merely
	// avoids aborting, and stays quiet, passes side 1 and fails here — and
	// that is the defect DR7 exists to prevent.
	require.Len(t, diags, 1)
	d := diags[0]
	assert.Equal(t, output.SeverityWarning, d.Severity, "warning, not error: the command carries on")
	assert.Equal(t, CodigoAvisoSSHConfig, d.Code)
	assert.Equal(t, caminho, d.File)
	assert.Equal(t, 6, d.Line, "the line of the Match the library does not understand")
	assert.Positive(t, d.Column)
	assert.Contains(t, d.Message, caminho)
	assert.Contains(t, d.Message, `unsupported Match criterion "user"`,
		"the message says what was not understood, not just that it failed")
	assert.Contains(t, d.Message, "--host", "and says what still works")
}

// TestResolverMatchExecDegradaComAviso covers the other rejected criterion —
// the library refuses `Match exec` on purpose, so as not to run a command out
// of an untrusted file, and ngx agrees with the refusal.
func TestResolverMatchExecDegradaComAviso(t *testing.T) {
	caminho := escreverConfig(t, "Match exec \"true\"\n  User deploy\n")

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)

	require.NoError(t, err)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityWarning, diags[0].Severity)
	assert.Equal(t, 1, diags[0].Line)
	assert.Equal(t, usuarioEsperado(t), opts.User)
}

func TestResolverArquivoIlegivelDegradaComAviso(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads any file; the case does not exist")
	}
	caminho := escreverConfig(t, "Host web1\n  User deploy\n")
	require.NoError(t, os.Chmod(caminho, 0o000))
	t.Cleanup(func() { _ = os.Chmod(caminho, 0o600) })

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)

	require.NoError(t, err)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityWarning, diags[0].Severity)
	assert.Equal(t, CodigoAvisoSSHConfig, diags[0].Code)
	assert.Equal(t, caminho, diags[0].File)
	assert.Zero(t, diags[0].Line, "no line when the problem is not on a line")
	assert.Equal(t, PortaSSHPadrao, opts.Port)
}

func TestResolverPortaInvalidaNoArquivoAvisaEUsaDefault(t *testing.T) {
	caminho := escreverConfig(t, "Host web1\n  Port setenta\n")

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)

	require.NoError(t, err)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityWarning, diags[0].Severity)
	assert.Contains(t, diags[0].Message, "setenta")
	assert.Equal(t, PortaSSHPadrao, opts.Port)
}

func TestResolverExpandeTilNoIdentityFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	caminho := escreverConfig(t, "Host web1\n  IdentityFile ~/.ssh/id_web1\n")

	opts, diags, err := ResolverSSHConfig(SSHOptions{Host: "web1"}, caminho)

	require.NoError(t, err)
	assert.Empty(t, diags)
	assert.Equal(t, filepath.Join(home, ".ssh", "id_web1"), opts.KeyPath)
}

func TestResolverPreservaCamposQueOArquivoNaoInfluencia(t *testing.T) {
	caminho := escreverConfig(t, "Host web1\n  User deploy\n")
	flags := SSHOptions{
		Host:            "web1",
		KnownHostsPath:  "/custom/known_hosts",
		InsecureHostKey: true,
		Password:        "segredo",
	}

	opts, _, err := ResolverSSHConfig(flags, caminho)

	require.NoError(t, err)
	assert.Equal(t, "/custom/known_hosts", opts.KnownHostsPath)
	assert.True(t, opts.InsecureHostKey)
	assert.Equal(t, "segredo", opts.Password)
}

func TestResolverSemHostEErroDeUso(t *testing.T) {
	caminho := escreverConfig(t, "Host web1\n  User deploy\n")

	_, diags, err := ResolverSSHConfig(SSHOptions{Host: "  "}, caminho)

	require.Error(t, err)
	assert.Equal(t, output.ExitUsage, output.CodeOf(err))
	assert.NotNil(t, diags, "the diagnostics list is never nil")
}
