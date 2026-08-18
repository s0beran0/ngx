package transport

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"

	"github.com/s0beran0/ngx/internal/output"
)

// Diagnostic codes for authentication (DR2).
//
// Only the first one is an error: having no ssh-agent, or not being able to
// use the key that was pointed at, is just one less method in the list. The
// command only stops when the list ends up empty — then there is nothing left
// to try against the server.
const (
	// CodigoSemMetodoAuth: no authentication method can be assembled.
	CodigoSemMetodoAuth = "NGX-0205"

	// CodigoAvisoSSHAgentAusente: there is no reachable ssh-agent. A normal
	// situation, reported because it changes what will be tried.
	CodigoAvisoSSHAgentAusente = "NGX-0212"

	// CodigoAvisoChaveIndisponivel: the key that was pointed at exists in
	// the configuration but cannot be used — missing file, invalid format,
	// or a passphrase there is no way to obtain.
	CodigoAvisoChaveIndisponivel = "NGX-0213"
)

// Names of the authentication methods, in the order they are tried. They show
// up in Autenticacao.Nomes so that whoever consumes the output knows what was
// offered to the server without having to infer it from a failure.
const (
	MetodoSSHAgent = "ssh-agent"
	MetodoChave    = "key"
	MetodoSenha    = "password"
)

// Environment variables the secrets may come from.
//
// A secret never comes from a flag. A flag shows up in `ps`, in the shell
// history and in the log of any CI: whoever passes a password by flag has
// already leaked it. The two accepted inputs are the environment and a
// terminal prompt — in that order — and any password flag added here must be
// rejected in review.
const (
	// EnvSenhaSSH carries the password of the user on the remote host.
	EnvSenhaSSH = "NGX_SSH_PASSWORD"

	// EnvPassphraseChaveSSH carries the passphrase that unlocks the private
	// key.
	EnvPassphraseChaveSSH = "NGX_SSH_KEY_PASSPHRASE"

	// EnvSocketSSHAgent is the standard OpenSSH variable pointing at the
	// ssh-agent channel. It is honored on every platform; on Windows, when
	// empty, there is a default named pipe (see agent_windows.go).
	EnvSocketSSHAgent = "SSH_AUTH_SOCK"
)

// errSSHAgentAusente marks the ssh-agent connection failures that are not an
// ngx error: there is no agent, or it does not answer. Having it as a sentinel
// makes it explicit, in agent_unix.go and agent_windows.go, that the failure
// path there is expected.
var errSSHAgentAusente = errors.New("ssh-agent unavailable")

// Autenticacao is the list of methods ngx offers the server, in DR2 order:
// ssh-agent, key file, password.
//
// The order is the main product of this type. The ssh-agent comes first
// because with it the private key is never read by ngx — it sends the
// challenge and gets back the signature —, and less of our code touching key
// material is less surface to get wrong.
//
// Metodos and Nomes are parallel: Nomes[i] describes Metodos[i]. Neither is
// nil.
type Autenticacao struct {
	Metodos []ssh.AuthMethod
	Nomes   []string

	fechar []func() error
}

// Close releases the resources opened while assembling — today, the ssh-agent
// connection. Call it after the handshake, and never before: the ssh-agent
// method queries the keys during authentication. Calling it twice is safe.
func (a *Autenticacao) Close() error {
	if a == nil {
		return nil
	}
	var erros []error
	for _, f := range a.fechar {
		if err := f(); err != nil {
			erros = append(erros, err)
		}
	}
	a.fechar = nil
	return errors.Join(erros...)
}

// ambienteAuth gathers the system edges the assembly touches: the ssh-agent,
// the environment, and the terminal. They sit behind fields so that the tests
// exercise the order of the methods with no socket, no real environment
// variable and — what matters most — no path that could block waiting for
// someone to type.
type ambienteAuth struct {
	conectarAgente  func() (net.Conn, error)
	lerEnv          func(string) string
	stdinEhTerminal func() bool
	lerSegredo      func(prompt string) (string, error)
	// home is injected so the test does not depend on the HOME of whoever
	// runs the suite: the search for default keys reads ~/.ssh, and a test
	// that could see the developer's real key would pass on their machine
	// and fail in CI.
	home func() (string, error)
}

func ambienteAuthPadrao() ambienteAuth {
	return ambienteAuth{
		conectarAgente:  conectarSSHAgent,
		lerEnv:          os.Getenv,
		stdinEhTerminal: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		lerSegredo:      lerSegredoDoTerminal,
		home:            os.UserHomeDir,
	}
}

// MontarAutenticacao assembles the authentication methods for the given
// options.
//
// It returns three things for the same reason VerificadorHostKey does: the
// list, the diagnostics of what was left out, and the error for when nothing
// was left. A method that cannot be assembled does not bring the connection
// down — it just does not enter the list, with a diagnostic saying why. No
// ssh-agent is not a failure; no method at all is.
//
// The list of diagnostics is never nil.
func MontarAutenticacao(opts SSHOptions) (*Autenticacao, []output.Diagnostic, error) {
	return montarAutenticacao(opts, ambienteAuthPadrao())
}

func montarAutenticacao(opts SSHOptions, amb ambienteAuth) (*Autenticacao, []output.Diagnostic, error) {
	auth := &Autenticacao{Metodos: []ssh.AuthMethod{}, Nomes: []string{}}
	diags := []output.Diagnostic{}

	adicionar := func(nome string, metodo ssh.AuthMethod, diag *output.Diagnostic) {
		if diag != nil {
			diags = append(diags, *diag)
		}
		if metodo != nil {
			auth.Metodos = append(auth.Metodos, metodo)
			auth.Nomes = append(auth.Nomes, nome)
		}
	}

	// DR2 order -- ssh-agent before key file -- with one exception measured
	// against a real server: when the user NAMES the key in --key, it comes
	// first.
	//
	// The reason is the sshd MaxAuthTries, 6 by default. Each ssh-agent key
	// spends one attempt, and a developer usually has several loaded. With
	// the agent in front, the explicitly requested key simply never gets
	// offered, and the server drops the connection with "no supported
	// methods remain" -- a message that does not point at the cause. It is
	// the same problem that ssh's IdentitiesOnly=yes solves.
	//
	// Without --key the original order holds: the agent is preferable
	// precisely because the private key is never read by ngx.
	chaveExplicita := opts.KeyPath != ""

	// ALL keys go into a SINGLE public key method, and the order inside it
	// is what decides preference.
	//
	// Measured against a real server: with the ssh-agent loaded, offering
	// agent and file as SEPARATE methods failed, while `ssh` connected with
	// the same keys. As soon as the first public key method is exhausted
	// without authenticating, the next one does not save the day. OpenSSH
	// does not suffer from this because it offers everything in a single
	// method -- and now ngx does too.
	//
	// The order: the key named in --key first, because the user named it;
	// then the ssh-agent, preferable because the private key is never read
	// by us; and finally the default keys in ~/.ssh, which are what makes
	// `ngx --host web1` work for whoever already has `ssh web1`.
	assinantes := []func() ([]ssh.Signer, error){}

	if chaveExplicita {
		metodo, diag := metodoChave(opts, amb)
		if diag != nil {
			diags = append(diags, *diag)
		}
		if metodo != nil {
			assinantes = append(assinantes, metodo)
			auth.Nomes = append(auth.Nomes, MetodoChave)
		}
	}

	if fonte, fechar, diag := assinantesDoAgente(amb); diag != nil || fonte != nil {
		if diag != nil {
			diags = append(diags, *diag)
		}
		if fechar != nil {
			auth.fechar = append(auth.fechar, fechar)
		}
		if fonte != nil {
			assinantes = append(assinantes, fonte)
			auth.Nomes = append(auth.Nomes, MetodoSSHAgent)
		}
	}

	if !chaveExplicita {
		metodo, diag := metodoChave(opts, amb)
		if diag != nil {
			diags = append(diags, *diag)
		}
		if metodo != nil {
			assinantes = append(assinantes, metodo)
			auth.Nomes = append(auth.Nomes, MetodoChave)
		}
	}

	if len(assinantes) > 0 {
		auth.Metodos = append(auth.Metodos, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			todos := []ssh.Signer{}
			for _, fonte := range assinantes {
				ss, err := fonte()
				if err != nil {
					// One source failing does not bring the others
					// down: an ssh-agent that died halfway cannot
					// keep the on-disk key from being offered.
					continue
				}
				todos = append(todos, ss...)
			}
			return todos, nil
		}))
	}

	adicionar(MetodoSenha, metodoSenha(opts, amb), nil)

	if len(auth.Metodos) == 0 {
		_ = auth.Close()
		return nil, diags, erroSemMetodoAuth(opts)
	}

	return auth, diags, nil
}

// assinantesDoAgente connects to the system ssh-agent and turns the client
// into an authentication method.
//
// It uses PublicKeysCallback, not PublicKeys: with the callback the key list
// is asked of the agent at authentication time, so a key added with `ssh-add`
// after ngx started is still seen.
//
// Not reaching the ssh-agent returns (nil, nil, warning). That is the most
// common case on a machine with no agent running and there is nothing wrong
// with it.
func assinantesDoAgente(amb ambienteAuth) (fonteAssinantes, func() error, *output.Diagnostic) {
	conn, err := amb.conectarAgente()
	if err != nil {
		d := avisoSSHAgentAusente(err)
		return nil, nil, &d
	}
	cliente := agent.NewClient(conn)
	return cliente.Signers, conn.Close, nil
}

// metodoChave reads the private key pointed at by opts.KeyPath.
//
// An encrypted key has three outcomes, in this order: the passphrase is in the
// environment, and the key is unlocked right away; standard input is a
// terminal, and the prompt is deferred to authentication time — so whoever
// already authenticated through the ssh-agent is never asked; or there is
// nowhere to get the passphrase from, and the method leaves the list with a
// warning naming the environment variable.
//
// The third case is what keeps ngx usable by an AI agent: running under a
// pipe, it fails fast instead of blocking on a keystroke that will never come.
// ChavesPadrao are the identity files OpenSSH tries when nobody points at one.
// The order is its own. `ssh` searching on its own is exactly what makes
// `ssh web1` work with no configuration, and DR2 promises that
// `ngx --host web1` works for whoever already has that — so ngx searches too.
//
// Measured against a real server: the key that authenticated was ~/.ssh/id_rsa,
// outside ~/.ssh/config and outside the ssh-agent, which only had certificates
// from another system. Without this search ngx failed where ssh connected.
//
// id_dsa is left out: OpenSSH disabled DSA by default, and offering a key the
// server refuses only spends one of the few MaxAuthTries attempts.
var ChavesPadrao = []string{"id_rsa", "id_ecdsa", "id_ed25519"}

func metodoChave(opts SSHOptions, amb ambienteAuth) (fonteAssinantes, *output.Diagnostic) {
	if opts.KeyPath == "" {
		return metodoChavesPadrao(amb)
	}

	pem, err := os.ReadFile(opts.KeyPath)
	if err != nil {
		d := avisoChaveIndisponivel(opts.KeyPath, fmt.Sprintf("the file could not be read (%v)", err))
		return nil, &d
	}

	signer, err := ssh.ParsePrivateKey(pem)
	if err == nil {
		return assinantesFixos(signer), nil
	}

	var faltaPassphrase *ssh.PassphraseMissingError
	if !errors.As(err, &faltaPassphrase) {
		d := avisoChaveIndisponivel(opts.KeyPath,
			fmt.Sprintf("the file is not a valid private key (%v)", err))
		return nil, &d
	}

	if passphrase := amb.lerEnv(EnvPassphraseChaveSSH); passphrase != "" {
		signer, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
		if err != nil {
			d := avisoChaveIndisponivel(opts.KeyPath,
				fmt.Sprintf("the passphrase in %s does not unlock the key (%v)", EnvPassphraseChaveSSH, err))
			return nil, &d
		}
		return assinantesFixos(signer), nil
	}

	if !amb.stdinEhTerminal() {
		d := avisoChaveIndisponivel(opts.KeyPath, fmt.Sprintf(
			"the key is protected by a passphrase and standard input is not a terminal, "+
				"so there is no way to ask; set %s in the environment to use this key",
			EnvPassphraseChaveSSH))
		return nil, &d
	}

	return func() ([]ssh.Signer, error) {
		passphrase, err := amb.lerSegredo(fmt.Sprintf("passphrase for key %s: ", opts.KeyPath))
		if err != nil {
			return nil, fmt.Errorf("could not read the passphrase for %s: %w", opts.KeyPath, err)
		}
		signer, err := ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("the passphrase provided does not unlock the key %s: %w", opts.KeyPath, err)
		}
		return []ssh.Signer{signer}, nil
	}, nil
}

// metodoSenha is the last resort in the order.
//
// The password comes from opts.Password — filled in from the environment by
// whoever assembled the options, never from a flag —, from the environment, or
// from a prompt. The prompt only exists when standard input is a terminal, and
// even then it is deferred to authentication time: if the server accepts the
// key, nobody is asked.
//
// With no terminal and no secret in the environment the method simply does not
// exist. There is never a block waiting for typing.
func metodoSenha(opts SSHOptions, amb ambienteAuth) ssh.AuthMethod {
	if opts.Password != "" {
		return ssh.Password(opts.Password)
	}
	if senha := amb.lerEnv(EnvSenhaSSH); senha != "" {
		return ssh.Password(senha)
	}
	if !amb.stdinEhTerminal() {
		return nil
	}
	return ssh.PasswordCallback(func() (string, error) {
		return amb.lerSegredo(fmt.Sprintf("password for %s: ", destinoLegivel(opts)))
	})
}

// lerSegredoDoTerminal asks for a secret with echo turned off.
//
// The prompt goes to stderr because stdout carries the JSON envelope: writing
// the prompt text there would corrupt the output another program is parsing.
func lerSegredoDoTerminal(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("standard input is not a terminal; set %s in the environment", EnvSenhaSSH)
	}
	fmt.Fprint(os.Stderr, prompt)
	segredo, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(segredo), nil
}

// avisoSSHAgentAusente reports that the ssh-agent was left out.
//
// Info severity, not warning: there is nothing to fix. The diagnostic exists
// because the list of offered methods changed, and whoever reads the output
// needs to be able to explain a refusal by the server without guessing.
func avisoSSHAgentAusente(causa error) output.Diagnostic {
	return output.Diagnostic{
		Severity: output.SeverityInfo,
		Code:     CodigoAvisoSSHAgentAusente,
		Message: fmt.Sprintf(
			"ssh-agent is not available (%v); ssh-agent authentication will not be tried. "+
				"This is not an error: if you want to use it, start the ssh-agent and register "+
				"the key with `ssh-add`",
			causa),
	}
}

// avisoChaveIndisponivel reports that the key pointed at did not enter the
// list.
//
// Warning severity, not info: someone pointed at a key — through --key or
// through IdentityFile in ~/.ssh/config — and it is not being used. Falling
// back to the password silently would make a wrong path look right.
func avisoChaveIndisponivel(caminho, motivo string) output.Diagnostic {
	return output.Diagnostic{
		Severity: output.SeverityWarning,
		Code:     CodigoAvisoChaveIndisponivel,
		Message: fmt.Sprintf(
			"the key %s will not be used for authentication: %s", caminho, motivo),
		File: caminho,
	}
}

// erroSemMetodoAuth is the only error of this stage: nothing was left to offer
// the server.
//
// Getting here implies that standard input is not a terminal — with a terminal
// there is always at least the password method —, so the message names the
// environment variable. This is exactly the case of an AI agent running ngx
// under a pipe: instead of blocking on a keystroke that never comes, it gets
// the instruction of what to set.
func erroSemMetodoAuth(opts SSHOptions) error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoSemMetodoAuth,
			Message: fmt.Sprintf(
				"no authentication method available for %s: the ssh-agent did not answer, "+
					"no usable key was provided, and standard input is not a terminal, "+
					"so ngx has no way to ask for the password. Pick one: start the ssh-agent "+
					"and register the key with `ssh-add`; point at a key without a passphrase "+
					"using --key (or set %s); or put the password in %s. The password is never "+
					"accepted by flag, because a flag shows up in `ps`, in the shell history "+
					"and in the CI log",
				destinoLegivel(opts), EnvPassphraseChaveSSH, EnvSenhaSSH),
		},
	}
}

// destinoLegivel describes the target the way the user recognizes it,
// "user@host".
func destinoLegivel(opts SSHOptions) string {
	switch {
	case opts.User != "" && opts.Host != "":
		return opts.User + "@" + opts.Host
	case opts.Host != "":
		return opts.Host
	default:
		return "the target"
	}
}

// metodoChavesPadrao assembles a method with the default keys that exist on
// disk and open without a passphrase.
//
// Without a passphrase on purpose: here the user did not ask for any key, so
// prompting for the password of a file they never mentioned would be
// intrusive, and under a pipe — which is how an AI agent runs this — there is
// nobody to ask. A key protected by a passphrase remains reachable through the
// ssh-agent, which is the recommended path, or through an explicit --key.
func metodoChavesPadrao(amb ambienteAuth) (fonteAssinantes, *output.Diagnostic) {
	if amb.home == nil {
		return nil, nil
	}
	home, err := amb.home()
	if err != nil {
		return nil, nil
	}

	signers := []ssh.Signer{}
	for _, nome := range ChavesPadrao {
		pem, err := os.ReadFile(filepath.Join(home, ".ssh", nome))
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(pem)
		if err != nil {
			continue
		}
		signers = append(signers, signer)
	}

	if len(signers) == 0 {
		return nil, nil
	}
	return assinantesFixos(signers...), nil
}

// fonteAssinantes hands keys to the handshake. It is a function, and not a
// ready-made list, because the ssh-agent may gain keys after ngx started and
// because a key with a passphrase should only ask for it if its turn actually
// comes.
type fonteAssinantes func() ([]ssh.Signer, error)

func assinantesFixos(ss ...ssh.Signer) fonteAssinantes {
	return func() ([]ssh.Signer, error) { return ss, nil }
}
