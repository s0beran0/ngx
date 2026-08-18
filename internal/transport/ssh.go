package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/s0beran0/ngx/internal/output"
)

const (
	// CodigoConexaoSSH marks the failure to establish the SSH session: DNS,
	// network, timeout, or the server refusing every authentication method.
	CodigoConexaoSSH = "NGX-0206"

	// CodigoSessaoSFTP marks the case where the SSH connection comes up but
	// the SFTP subsystem does not. They are different problems with
	// different solutions: the first is network or credentials, the second
	// is sshd configuration.
	CodigoSessaoSFTP = "NGX-0207"
)

// TimeoutSSHPadrao caps the handshake when SSHOptions.Timeout says nothing.
// Without a timeout, a host that accepts the TCP connection and never answers
// the handshake leaves ngx hanging forever — and whoever is waiting for the
// output has no way of knowing whether the command is slow or dead.
const TimeoutSSHPadrao = 30 * time.Second

// metacaracteresGlob are the characters that make a pattern a pattern. The
// backslash is in there because path.Match treats it as an escape: a pattern
// containing one has to go through expansion, not through the Lstat shortcut.
const metacaracteresGlob = `*?[\`

// leitorRemoto is the subset of *sftp.Client that pattern expansion uses.
//
// It exists as an interface for one reason only: the DR6 home-grown glob is
// the part of this layer that needs real testing, and a test that requires a
// live SFTP server does not exercise the case that matters — an I/O error in
// the middle of the listing. With the interface, that error is injected
// directly.
type leitorRemoto interface {
	ReadDir(p string) ([]os.FileInfo, error)
	Lstat(p string) (os.FileInfo, error)
}

// sshTransport operates a remote host: files over SFTP, commands over an exec
// session. Nothing is installed on the target (DR3) — ngx reads what is
// already there and runs the binary that already exists.
type sshTransport struct {
	cliente *ssh.Client
	arquivo *sftp.Client

	// destino is "user@host:port", the form Describe publishes in the
	// envelope.
	destino string

	umaVez     sync.Once
	erroFechar error
}

// SSH connects to the host described by opts and returns the remote transport.
//
// It discards the connection diagnostics. Use SSHComDiagnosticos in any path
// that builds an envelope: the --insecure-host-key warning and the missing
// ssh-agent one explain to whoever reads the output what ngx operated against,
// and losing them makes the DR1 escape hatch silent.
func SSH(opts SSHOptions) (Transport, error) {
	tr, _, err := SSHComDiagnosticos(opts)
	return tr, err
}

// SSHComDiagnosticos connects and also returns what the assembly observed
// along the way: host key accepted without verification, ssh-agent
// unavailable, unreadable key. None of these brings the connection down, and
// none of them may disappear.
//
// The list of diagnostics is never nil.
func SSHComDiagnosticos(opts SSHOptions) (Transport, []output.Diagnostic, error) {
	diags := []output.Diagnostic{}

	host := strings.TrimSpace(opts.Host)
	if host == "" {
		return nil, diags, output.Usage("no target host provided")
	}

	porta := opts.Port
	if porta == 0 {
		porta = PortaSSHPadrao
	}
	usuario := opts.User
	if usuario == "" {
		usuario = usuarioCorrente()
	}

	verificar, diagsHost, err := VerificadorHostKey(opts)
	if len(diagsHost) > 0 {
		diags = append(diags, diagsHost...)
	}
	if err != nil {
		return nil, diags, err
	}

	auth, diagsAuth, err := MontarAutenticacao(opts)
	if len(diagsAuth) > 0 {
		diags = append(diags, diagsAuth...)
	}
	if err != nil {
		return nil, diags, err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = TimeoutSSHPadrao
	}

	endereco := net.JoinHostPort(host, strconv.Itoa(porta))
	conf := &ssh.ClientConfig{
		User:            usuario,
		Auth:            auth.Metodos,
		HostKeyCallback: verificar,
		Timeout:         timeout,
	}
	cliente, err := ssh.Dial("tcp", endereco, conf)

	// A server usually offers several host key types and the client picks
	// one. If known_hosts recorded the host under another type, the
	// verification fails without anything being wrong -- and `ssh` itself
	// solves this by restricting the negotiation to the types it already
	// knows.
	//
	// There is no way to restrict before knowing which ones those are, and
	// finding out requires the error. So the second attempt happens only
	// here, only when the outcome was exactly that, and only once. Nothing
	// is loosened: the same verification runs again, just over an algorithm
	// that known_hosts covers.
	if err != nil {
		if tipos := tiposRegistrados(err); len(tipos) > 0 {
			conf.HostKeyAlgorithms = tipos
			cliente, err = ssh.Dial("tcp", endereco, conf)
		}
	}

	// The ssh-agent connection is only useful during the handshake; after
	// it, it is an open socket with no use. The error from closing does not
	// become a diagnostic because it changes nothing for the caller: the
	// handshake has either already happened or already failed.
	_ = auth.Close()

	if err != nil {
		// The host key error already comes typed from the callback (DR1).
		// Rewrapping it in CodigoConexaoSSH would erase the distinction
		// between first access and changed key — which is exactly what
		// whoever consumes the output has to separate without interpreting
		// text —, and would turn a verification refusal into a generic
		// network or credential failure.
		var tipado *output.Error
		if errors.As(err, &tipado) {
			return nil, diags, tipado
		}
		return nil, diags, erroConexaoSSH(usuario, endereco, auth.Nomes, err)
	}

	arquivo, err := sftp.NewClient(cliente)
	if err != nil {
		_ = cliente.Close()
		return nil, diags, erroSessaoSFTP(usuario, endereco, err)
	}

	return &sshTransport{
		cliente: cliente,
		arquivo: arquivo,
		destino: fmt.Sprintf("%s@%s", usuario, endereco),
	}, diags, nil
}

func (t *sshTransport) Open(caminho string) (io.ReadCloser, error) {
	// No direct `return t.arquivo.Open(...)`: on the error path that would
	// return a non-nil interface holding a nil *sftp.File, and whoever
	// checks `rc != nil` before `err` would panic.
	f, err := t.arquivo.Open(caminho)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (t *sshTransport) Glob(padrao string) ([]string, error) {
	return globRemoto(t.arquivo, padrao)
}

func (t *sshTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	if len(argv) == 0 {
		return nil, nil, 0, errors.New("transport: empty argv")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, err
	}

	sessao, err := t.cliente.NewSession()
	if err != nil {
		// Opening a channel fails when the connection is already down:
		// transport, not the command's verdict.
		return nil, nil, 0, err
	}
	defer func() { _ = sessao.Close() }()

	var stdout, stderr bytes.Buffer
	sessao.Stdout = &stdout
	sessao.Stderr = &stderr

	comando := montarLinhaDeComando(argv)

	fim := make(chan error, 1)
	go func() { fim <- sessao.Run(comando) }()

	select {
	case err := <-fim:
		return classificarSaidaSSH(stdout.Bytes(), stderr.Bytes(), err)
	case <-ctx.Done():
		// Best effort: ask the server to kill the process and tear down
		// the channel, which is what releases Run.
		_ = sessao.Signal(ssh.SIGKILL)
		_ = sessao.Close()
		// Wait for the goroutine before touching the buffers. Reading
		// them while the session copy is still writing would be a data
		// race.
		<-fim
		return stdout.Bytes(), stderr.Bytes(), 0, ctx.Err()
	}
}

func (t *sshTransport) Close() error {
	t.umaVez.Do(func() {
		var erros []error
		if t.arquivo != nil {
			if err := t.arquivo.Close(); err != nil {
				erros = append(erros, err)
			}
		}
		if t.cliente != nil {
			if err := t.cliente.Close(); err != nil {
				erros = append(erros, err)
			}
		}
		t.erroFechar = errors.Join(erros...)
	})
	return t.erroFechar
}

func (t *sshTransport) Describe() string {
	return "ssh://" + t.destino
}

// classificarSaidaSSH applies the central Transport rule to the outcome of a
// remote session.
//
// *ssh.ExitError is the server reporting the command's exit code: it ran to
// completion and rejected, which is a result and not an error. An `nginx -t`
// that rejects the configuration comes through here.
//
// *ssh.ExitMissingError is the opposite: the session ended without the server
// saying how. That is what happens when the connection drops halfway, and in
// that case there is no exit code — returning zero with a nil err would make
// an interrupted command look like success. An I/O error reads the same way.
func classificarSaidaSSH(stdout, stderr []byte, err error) ([]byte, []byte, int, error) {
	if err == nil {
		return stdout, stderr, 0, nil
	}

	var saida *ssh.ExitError
	if errors.As(err, &saida) {
		return stdout, stderr, saida.ExitStatus(), nil
	}

	return stdout, stderr, 0, err
}

// montarLinhaDeComando turns argv into the string the SSH exec channel
// accepts.
//
// The SSH protocol has no way of sending an argv: the "exec" request carries a
// string, and the server hands it to the user's login shell. Since the string
// is unavoidable, what prevents injection is escaping per argument — each argv
// element becomes a token the shell cannot reinterpret. Joining argv with
// spaces, unquoted, would be the same as running a shell with user input.
func montarLinhaDeComando(argv []string) string {
	partes := make([]string, 0, len(argv))
	for _, arg := range argv {
		partes = append(partes, escaparArgumento(arg))
	}
	return strings.Join(partes, " ")
}

// escaparArgumento wraps the argument in single quotes, the only POSIX shell
// quoting that interprets absolutely nothing inside — no $, no backtick, no
// backslash.
//
// The single quote itself is the only character that cannot appear inside it:
// the quoting is closed, the quote is escaped outside with a backslash, and
// the quoting is reopened. An empty argument becomes an empty pair of quotes
// and remains an argument, instead of vanishing from the line.
func escaparArgumento(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// globRemoto expands a path pattern on the remote host over ReadDir and
// path.Match, propagating I/O errors as errors (DR6).
//
// sftp.Client has a Glob, and it does not serve: the comment in its own source
// says it ignores filesystem errors and that the only possible error is
// ErrBadPattern. On the path without metacharacters it is literal — if Lstat
// fails, it returns (nil, nil). Over an unstable link, `include conf.d/*.conf`
// would return zero files with no signal at all, and ngx would present the
// server configuration without the files it actually has. A tool read by an AI
// agent cannot be confidently incomplete: the consumer has no way to suspect
// it.
//
// The only failure that does not become an error is absence: a directory that
// does not exist means the pattern matches nothing, and that is a legitimate
// answer. Lack of permission becomes an error, because "I could not read" is
// not "it does not exist".
//
// A remote path is always POSIX: it uses path, never filepath. With filepath,
// ngx running on Windows would expand conf.d\*.conf against a Linux server.
//
// The structure deliberately follows the one in the stdlib filepath.Glob --
// same resolution order, same protection against infinite recursion, same
// matching semantics. What changes is the error handling.
//
// With no match it returns an empty list and a nil err, never nil.
func globRemoto(remoto leitorRemoto, padrao string) ([]string, error) {
	// Validate the syntax before touching the network: a malformed pattern
	// is a usage error, not a reason for a round trip to the server.
	if _, err := path.Match(padrao, ""); err != nil {
		return nil, err
	}

	if !temMetacaractereGlob(padrao) {
		if _, err := remoto.Lstat(padrao); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return []string{}, nil
			}
			return nil, err
		}
		return []string{padrao}, nil
	}

	dir, arquivo := path.Split(padrao)
	dir = limparDirGlob(dir)

	if !temMetacaractereGlob(dir) {
		return globNoDiretorio(remoto, dir, arquivo, []string{})
	}

	// Protects against infinite recursion: a pattern that reduces to itself
	// (which a loose backslash can produce) would never converge.
	if dir == padrao {
		return nil, path.ErrBadPattern
	}

	diretorios, err := globRemoto(remoto, dir)
	if err != nil {
		return nil, err
	}

	achados := []string{}
	for _, d := range diretorios {
		achados, err = globNoDiretorio(remoto, d, arquivo, achados)
		if err != nil {
			return nil, err
		}
	}
	return achados, nil
}

// globNoDiretorio appends to achados the entries of dir that match the
// pattern.
//
// The sorting is deliberate: the SFTP ReadDir hands back whatever the server
// sends, in whatever order it likes, and the list of include files feeds the
// canonical hash of the configuration. An unstable order would become an
// unstable hash.
func globNoDiretorio(remoto leitorRemoto, dir, padrao string, achados []string) ([]string, error) {
	entradas, err := remoto.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A nonexistent directory is absence of matches, not a
			// failure: the pattern simply matches nothing.
			return achados, nil
		}
		return nil, err
	}

	nomes := make([]string, 0, len(entradas))
	for _, e := range entradas {
		nomes = append(nomes, e.Name())
	}
	sort.Strings(nomes)

	for _, nome := range nomes {
		casa, err := path.Match(padrao, nome)
		if err != nil {
			return nil, err
		}
		if casa {
			achados = append(achados, path.Join(dir, nome))
		}
	}
	return achados, nil
}

func temMetacaractereGlob(p string) bool {
	return strings.ContainsAny(p, metacaracteresGlob)
}

// limparDirGlob normalizes the directory half returned by path.Split, which
// always comes with a trailing slash. A relative pattern has an empty
// directory and becomes ".", which is what ReadDir expects.
func limparDirGlob(dir string) string {
	switch dir {
	case "":
		return "."
	case "/":
		return "/"
	default:
		return strings.TrimSuffix(dir, "/")
	}
}

// erroConexaoSSH describes the handshake failure naming the authentication
// methods that were offered.
//
// The list of methods is what separates "the network does not reach it" from
// "the network reaches it and the server accepted nothing I had". Without it,
// a refusal caused by a missing key in the ssh-agent is indistinguishable from
// a host that is down, and whoever reads the output has no way of choosing
// what to fix.
func erroConexaoSSH(usuario, endereco string, metodos []string, causa error) error {
	oferecidos := "none"
	if len(metodos) > 0 {
		oferecidos = strings.Join(metodos, ", ")
	}

	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoConexaoSSH,
			Message: fmt.Sprintf(
				"could not connect to %s@%s: %v. Authentication methods "+
					"offered: %s",
				usuario, endereco, causa, oferecidos),
		},
		Err: causa,
	}
}

// erroSessaoSFTP describes the case where SSH comes up and SFTP does not. The
// distinction matters because the fix lives in the server's sshd, not in the
// caller's credentials.
func erroSessaoSFTP(usuario, endereco string, causa error) error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoSessaoSFTP,
			Message: fmt.Sprintf(
				"the connection to %s@%s was established, but the SFTP subsystem did "+
					"not answer: %v. ngx reads the configuration over SFTP; check whether "+
					"the server's sshd has the subsystem enabled",
				usuario, endereco, causa),
		},
		Err: causa,
	}
}

// tiposRegistrados returns the key types known_hosts has for the host, and
// only when the failure was "it presented a type that is not on record". It
// returns nothing for a genuinely changed key or an unknown host: in those
// cases repeating the handshake would be bypassing the verification, not
// adjusting it.
func tiposRegistrados(err error) []string {
	var chave *knownhosts.KeyError
	if !errors.As(err, &chave) || len(chave.Want) == 0 {
		return nil
	}

	tipos := make([]string, 0, len(chave.Want))
	vistos := map[string]bool{}
	for i := range chave.Want {
		t := chave.Want[i].Key.Type()
		if !vistos[t] {
			vistos[t] = true
			tipos = append(tipos, t)
		}
	}
	return tipos
}
