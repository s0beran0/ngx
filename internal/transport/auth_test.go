package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/s0beran0/ngx/internal/output"
)

// No test in this file opens a socket, brings up a server or waits for
// keystrokes. ambienteAuth exists for that: assembling the methods is a pure
// decision about what is available, and that decision is what is verified
// here.

// ambienteVazio is the worst case: no ssh-agent, no environment variables and
// no terminal. lerSegredo fails the test if it is called — a prompt here would
// mean ngx would block waiting for someone to type into a pipe.
func ambienteVazio(t *testing.T) ambienteAuth {
	t.Helper()
	return ambienteAuth{
		conectarAgente: func() (net.Conn, error) {
			return nil, errors.New("SSH_AUTH_SOCK is not set in the environment")
		},
		lerEnv:          func(string) string { return "" },
		stdinEhTerminal: func() bool { return false },
		lerSegredo: func(prompt string) (string, error) {
			t.Fatalf("secret prompt fired with no terminal: %q", prompt)
			return "", nil
		},
	}
}

// comSSHAgent returns an environment where the ssh-agent answers. The
// connection is a net.Pipe that is never used: agent.NewClient does no I/O at
// construction time and the method only queries the agent during the
// handshake, which these tests do not perform.
func comSSHAgent(t *testing.T, amb ambienteAuth) ambienteAuth {
	t.Helper()
	amb.conectarAgente = func() (net.Conn, error) {
		nossa, deles := net.Pipe()
		t.Cleanup(func() { _ = deles.Close() })
		return nossa, nil
	}
	return amb
}

func comEnv(amb ambienteAuth, pares map[string]string) ambienteAuth {
	amb.lerEnv = func(nome string) string { return pares[nome] }
	return amb
}

// escreverChave writes an ed25519 key to disk. With an empty passphrase the
// key comes out in the clear.
func escreverChave(t *testing.T, passphrase string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var bloco *pem.Block
	if passphrase == "" {
		bloco, err = ssh.MarshalPrivateKey(priv, "ngx-test")
	} else {
		bloco, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "ngx-test", []byte(passphrase))
	}
	require.NoError(t, err)

	caminho := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(caminho, pem.EncodeToMemory(bloco), 0o600))
	return caminho
}

func diagnosticoComCodigo(diags []output.Diagnostic, codigo string) *output.Diagnostic {
	for i := range diags {
		if diags[i].Code == codigo {
			return &diags[i]
		}
	}
	return nil
}

// The order is the central contract of DR2, with the exception of the named
// key.
//
// When the user passes --key, it comes BEFORE the ssh-agent. The reason was
// measured against a real sshd: the default MaxAuthTries is 6, each ssh-agent
// key spends one attempt, and a developer usually has several loaded -- so the
// explicitly requested key never got offered and the connection died with "no
// supported methods remain", a message that does not point at the cause. It is
// the same problem IdentitiesOnly=yes solves in ssh.
//
// Without --key, the ssh-agent stays in front, which is preferable: with it
// the private key is never read by ngx.
func TestMontarAutenticacaoChaveNomeadaVemAntesDoAgente(t *testing.T) {
	amb := comSSHAgent(t, comEnv(ambienteVazio(t), map[string]string{
		EnvSenhaSSH: "s3nha",
	}))

	auth, diags, err := montarAutenticacao(SSHOptions{
		Host:    "web1",
		User:    "deploy",
		KeyPath: escreverChave(t, ""),
	}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoChave, MetodoSSHAgent, MetodoSenha}, auth.Nomes,
		"a key named in --key precedes the ssh-agent so it does not hit MaxAuthTries")

	// Two key sources, ONE public key method (plus the password one).
	//
	// Measured against a real server: offering agent and file as separate
	// methods fails when the agent has keys and none of them serves -- as
	// soon as the first public key method is exhausted, the next one does
	// not save the day, and ngx refused a connection that `ssh` made.
	// OpenSSH offers everything in a single method; this require is what
	// keeps the split from coming back.
	require.Len(t, auth.Metodos, 2,
		"all keys must fit into a single public key method")
	require.NotNil(t, diags)
	require.Empty(t, diags)
}

func TestMontarAutenticacaoSemChaveNomeadaMantemAgenteNaFrente(t *testing.T) {
	amb := comSSHAgent(t, comEnv(ambienteVazio(t), map[string]string{
		EnvSenhaSSH: "s3nha",
	}))

	auth, _, err := montarAutenticacao(SSHOptions{
		Host: "web1",
		User: "deploy",
	}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSSHAgent, MetodoSenha}, auth.Nomes,
		"without --key the ssh-agent comes first: the private key is never read by ngx")
}

// The absence of an ssh-agent is not an error: it is one less method, with an
// informational diagnostic. Confusing the two would make ngx fail on any
// machine that does not have the agent running.
func TestSSHAgentAusenteNaoEErro(t *testing.T) {
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "s3nha"})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", User: "deploy"}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)

	d := diagnosticoComCodigo(diags, CodigoAvisoSSHAgentAusente)
	require.NotNil(t, d, "the absence of the ssh-agent has to be reported, not silent")
	require.Equal(t, output.SeverityInfo, d.Severity,
		"having no ssh-agent is a normal situation; error severity would make it look like a defect")
	require.Contains(t, d.Message, "ssh-add")
}

// The password comes from the environment, and coming from the environment
// waives any prompt: the lerSegredo of ambienteVazio fails the test if called.
func TestSenhaVemDoAmbiente(t *testing.T) {
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "s3nha"})

	auth, _, err := montarAutenticacao(SSHOptions{Host: "web1", User: "deploy"}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)
}

// Running under a pipe — which is how an AI agent uses ngx — there is no way
// to ask anything. The command has to fail with an actionable instruction, and
// not hang. The test completing is the proof: if it hung, it would blow the
// go test timeout.
func TestSemTerminalESemAmbienteFalhaComMensagemClara(t *testing.T) {
	auth, diags, err := montarAutenticacao(
		SSHOptions{Host: "web1", User: "deploy"}, ambienteVazio(t))
	require.Error(t, err)
	require.Nil(t, auth)
	require.NotNil(t, diags)

	var saida *output.Error
	require.ErrorAs(t, err, &saida)
	require.Equal(t, CodigoSemMetodoAuth, saida.Diag.Code)
	require.Equal(t, output.SeverityError, saida.Diag.Severity)
	require.Contains(t, saida.Diag.Message, EnvSenhaSSH,
		"the message has to say which environment variable to set")
	require.Contains(t, saida.Diag.Message, "deploy@web1")
}

// With a terminal there is a password method, but the prompt is deferred to
// the handshake: if the server accepts the key, nobody is asked. Assembling
// the list can never read a secret.
func TestComTerminalNaoPerguntaNaMontagem(t *testing.T) {
	amb := ambienteVazio(t)
	amb.stdinEhTerminal = func() bool { return true }
	amb.lerSegredo = func(prompt string) (string, error) {
		t.Fatalf("prompt fired during assembly, before any handshake: %q", prompt)
		return "", nil
	}

	auth, _, err := montarAutenticacao(SSHOptions{Host: "web1", User: "deploy"}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)
}

// An encrypted key with the passphrase in the environment is unlocked at
// assembly time, with no prompt.
func TestChaveCifradaComPassphraseNoAmbiente(t *testing.T) {
	caminho := escreverChave(t, "abre-te")
	amb := comEnv(ambienteVazio(t), map[string]string{EnvPassphraseChaveSSH: "abre-te"})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", KeyPath: caminho}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoChave}, auth.Nomes)
	require.Nil(t, diagnosticoComCodigo(diags, CodigoAvisoChaveIndisponivel))
}

// An encrypted key, with no passphrase in the environment and no terminal: the
// method leaves the list with a warning naming the variable. No prompt under a
// pipe.
func TestChaveCifradaSemTerminalSaiDaListaComAviso(t *testing.T) {
	caminho := escreverChave(t, "abre-te")
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "s3nha"})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", KeyPath: caminho}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)

	d := diagnosticoComCodigo(diags, CodigoAvisoChaveIndisponivel)
	require.NotNil(t, d)
	require.Equal(t, output.SeverityWarning, d.Severity)
	require.Contains(t, d.Message, EnvPassphraseChaveSSH)
	require.Equal(t, caminho, d.File)
}

// A key that was pointed at and does not exist does not bring the connection
// down: it becomes a warning and the order goes on. But it cannot be silent —
// falling back to the password quietly would make a wrong path look right.
func TestChaveInexistenteViraAvisoENaoErro(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "nao-existe")
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "s3nha"})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", KeyPath: caminho}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)

	d := diagnosticoComCodigo(diags, CodigoAvisoChaveIndisponivel)
	require.NotNil(t, d)
	require.Equal(t, output.SeverityWarning, d.Severity)
}

// A file that is no key at all also leaves with a warning, and not an error.
func TestChaveMalformadaViraAviso(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "lixo")
	require.NoError(t, os.WriteFile(caminho, []byte("this is not a key"), 0o600))
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "s3nha"})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", KeyPath: caminho}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)
	require.NotNil(t, diagnosticoComCodigo(diags, CodigoAvisoChaveIndisponivel))
}

// A wrong passphrase in the environment cannot become a mute attempt: a
// warning comes out saying the variable does not unlock the key.
func TestPassphraseErradaNoAmbienteViraAviso(t *testing.T) {
	caminho := escreverChave(t, "abre-te")
	amb := comEnv(ambienteVazio(t), map[string]string{
		EnvPassphraseChaveSSH: "errada",
		EnvSenhaSSH:           "s3nha",
	})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", KeyPath: caminho}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)

	d := diagnosticoComCodigo(diags, CodigoAvisoChaveIndisponivel)
	require.NotNil(t, d)
	require.Contains(t, d.Message, EnvPassphraseChaveSSH)
}

// No secret may escape through a diagnostic or an error message. Whoever reads
// the ngx output is not necessarily entitled to the secret.
func TestSegredoNaoVazaEmDiagnostico(t *testing.T) {
	const senha = "s3nha-supersecreta"
	const passphrase = "passphrase-supersecreta"

	caminho := escreverChave(t, "abre-te")
	amb := comEnv(ambienteVazio(t), map[string]string{
		EnvSenhaSSH:           senha,
		EnvPassphraseChaveSSH: passphrase,
	})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1", KeyPath: caminho}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	for _, d := range diags {
		require.NotContains(t, d.Message, senha)
		require.NotContains(t, d.Message, passphrase)
	}
}

// The ssh-agent method holds the connection open until the end of the
// handshake; Close is what gives it back, and calling it twice has to be safe.
func TestCloseEIdempotente(t *testing.T) {
	amb := comSSHAgent(t, ambienteVazio(t))

	auth, _, err := montarAutenticacao(SSHOptions{Host: "web1"}, amb)
	require.NoError(t, err)

	require.NoError(t, auth.Close())
	require.NoError(t, auth.Close())

	var nulo *Autenticacao
	require.NoError(t, nulo.Close())
}

// The assembly never returns a nil list or nil names: an agent calling .length
// on a null list breaks.
func TestListasNuncaSaoNulas(t *testing.T) {
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "s3nha"})

	auth, diags, err := montarAutenticacao(SSHOptions{Host: "web1"}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.NotNil(t, auth.Metodos)
	require.NotNil(t, auth.Nomes)
	require.NotNil(t, diags)
}

// A password already resolved by the caller beats the environment. The field
// exists to receive what came from the environment, never from a flag.
func TestSenhaDasOpcoesVenceOAmbiente(t *testing.T) {
	amb := comEnv(ambienteVazio(t), map[string]string{EnvSenhaSSH: "do-ambiente"})

	auth, _, err := montarAutenticacao(
		SSHOptions{Host: "web1", Password: "das-opcoes"}, amb)
	require.NoError(t, err)
	t.Cleanup(func() { _ = auth.Close() })

	require.Equal(t, []string{MetodoSenha}, auth.Nomes)
}

// No password flag may exist on the package surface: a flag shows up in `ps`,
// in the shell history and in the CI log. The test reads the source itself
// because the defect it prevents is somebody adding the flag later.
func TestNenhumaFlagDeSenhaNoPacote(t *testing.T) {
	entradas, err := os.ReadDir(".")
	require.NoError(t, err)

	proibidos := []string{"--password", "--senha", "--passphrase", "--key-passphrase"}
	for _, e := range entradas {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		conteudo, err := os.ReadFile(e.Name())
		require.NoError(t, err)
		for _, p := range proibidos {
			require.NotContains(t, string(conteudo), p,
				"%s mentions %s: a secret never comes from a flag", e.Name(), p)
		}
	}
}
