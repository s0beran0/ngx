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

// DefaultSSHTimeout caps the handshake when SSHOptions.Timeout says nothing.
// Without a timeout, a host that accepts the TCP connection and never answers
// the handshake leaves ngx hanging forever — and whoever is waiting for the
// output has no way of knowing whether the command is slow or dead.
const DefaultSSHTimeout = 30 * time.Second

// globMetacharacters are the characters that make a pattern a pattern. The
// backslash is in there because path.Match treats it as an escape: a pattern
// containing one has to go through expansion, not through the Lstat shortcut.
const globMetacharacters = `*?[\`

// remoteReader is the subset of *sftp.Client that pattern expansion uses.
//
// It exists as an interface for one reason only: the DR6 home-grown glob is
// the part of this layer that needs real testing, and a test that requires a
// live SFTP server does not exercise the case that matters — an I/O error in
// the middle of the listing. With the interface, that error is injected
// directly.
type remoteReader interface {
	ReadDir(p string) ([]os.FileInfo, error)
	Lstat(p string) (os.FileInfo, error)
}

// sshTransport operates a remote host: files over SFTP, commands over an exec
// session. Nothing is installed on the target (DR3) — ngx reads what is
// already there and runs the binary that already exists.
type sshTransport struct {
	client     *ssh.Client
	sftpClient *sftp.Client

	// target is "user@host:port", the form Describe publishes in the
	// envelope.
	target string

	closeOnce sync.Once
	closeErr  error
}

// SSH connects to the host described by opts and returns the remote transport.
//
// It discards the connection diagnostics. Use SSHWithDiagnostics in any path
// that builds an envelope: the --insecure-host-key warning and the missing
// ssh-agent one explain to whoever reads the output what ngx operated against,
// and losing them makes the DR1 escape hatch silent.
func SSH(opts SSHOptions) (Transport, error) {
	tr, _, err := SSHWithDiagnostics(opts)
	return tr, err
}

// SSHWithDiagnostics connects and also returns what the assembly observed
// along the way: host key accepted without verification, ssh-agent
// unavailable, unreadable key. None of these brings the connection down, and
// none of them may disappear.
//
// The list of diagnostics is never nil.
func SSHWithDiagnostics(opts SSHOptions) (Transport, []output.Diagnostic, error) {
	diags := []output.Diagnostic{}

	host := strings.TrimSpace(opts.Host)
	if host == "" {
		return nil, diags, output.Usage("no target host provided")
	}

	port := opts.Port
	if port == 0 {
		port = DefaultSSHPort
	}
	user := opts.User
	if user == "" {
		user = currentUser()
	}

	verifyHostKey, hostKeyDiags, err := VerificadorHostKey(opts)
	if len(hostKeyDiags) > 0 {
		diags = append(diags, hostKeyDiags...)
	}
	if err != nil {
		return nil, diags, err
	}

	auth, authDiags, err := BuildAuthentication(opts)
	if len(authDiags) > 0 {
		diags = append(diags, authDiags...)
	}
	if err != nil {
		return nil, diags, err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultSSHTimeout
	}

	address := net.JoinHostPort(host, strconv.Itoa(port))
	conf := &ssh.ClientConfig{
		User:            user,
		Auth:            auth.Metodos,
		HostKeyCallback: verifyHostKey,
		Timeout:         timeout,
	}
	client, err := ssh.Dial("tcp", address, conf)

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
		if types := recordedKeyTypes(err); len(types) > 0 {
			conf.HostKeyAlgorithms = types
			client, err = ssh.Dial("tcp", address, conf)
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
		var typed *output.Error
		if errors.As(err, &typed) {
			return nil, diags, typed
		}
		return nil, diags, sshConnectionError(user, address, auth.Nomes, err)
	}

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return nil, diags, sftpSessionError(user, address, err)
	}

	return &sshTransport{
		client:     client,
		sftpClient: sftpClient,
		target:     fmt.Sprintf("%s@%s", user, address),
	}, diags, nil
}

func (t *sshTransport) Open(name string) (io.ReadCloser, error) {
	// No direct `return t.sftpClient.Open(...)`: on the error path that would
	// return a non-nil interface holding a nil *sftp.File, and whoever
	// checks `rc != nil` before `err` would panic.
	f, err := t.sftpClient.Open(name)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (t *sshTransport) Glob(pattern string) ([]string, error) {
	return remoteGlob(t.sftpClient, pattern)
}

func (t *sshTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	if len(argv) == 0 {
		return nil, nil, 0, errors.New("transport: empty argv")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, err
	}

	session, err := t.client.NewSession()
	if err != nil {
		// Opening a channel fails when the connection is already down:
		// transport, not the command's verdict.
		return nil, nil, 0, err
	}
	defer func() { _ = session.Close() }()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	command := buildCommandLine(argv)

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case err := <-done:
		return classifySSHExit(stdout.Bytes(), stderr.Bytes(), err)
	case <-ctx.Done():
		// Best effort: ask the server to kill the process and tear down
		// the channel, which is what releases Run.
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		// Wait for the goroutine before touching the buffers. Reading
		// them while the session copy is still writing would be a data
		// race.
		<-done
		return stdout.Bytes(), stderr.Bytes(), 0, ctx.Err()
	}
}

func (t *sshTransport) Close() error {
	t.closeOnce.Do(func() {
		var errs []error
		if t.sftpClient != nil {
			if err := t.sftpClient.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if t.client != nil {
			if err := t.client.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		t.closeErr = errors.Join(errs...)
	})
	return t.closeErr
}

func (t *sshTransport) Describe() string {
	return "ssh://" + t.target
}

// classifySSHExit applies the central Transport rule to the outcome of a
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
func classifySSHExit(stdout, stderr []byte, err error) ([]byte, []byte, int, error) {
	if err == nil {
		return stdout, stderr, 0, nil
	}

	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return stdout, stderr, exitErr.ExitStatus(), nil
	}

	return stdout, stderr, 0, err
}

// buildCommandLine turns argv into the string the SSH exec channel
// accepts.
//
// The SSH protocol has no way of sending an argv: the "exec" request carries a
// string, and the server hands it to the user's login shell. Since the string
// is unavoidable, what prevents injection is escaping per argument — each argv
// element becomes a token the shell cannot reinterpret. Joining argv with
// spaces, unquoted, would be the same as running a shell with user input.
func buildCommandLine(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, quoteArgument(arg))
	}
	return strings.Join(parts, " ")
}

// quoteArgument wraps the argument in single quotes, the only POSIX shell
// quoting that interprets absolutely nothing inside — no $, no backtick, no
// backslash.
//
// The single quote itself is the only character that cannot appear inside it:
// the quoting is closed, the quote is escaped outside with a backslash, and
// the quoting is reopened. An empty argument becomes an empty pair of quotes
// and remains an argument, instead of vanishing from the line.
func quoteArgument(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// remoteGlob expands a path pattern on the remote host over ReadDir and
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
func remoteGlob(remote remoteReader, pattern string) ([]string, error) {
	// Validate the syntax before touching the network: a malformed pattern
	// is a usage error, not a reason for a round trip to the server.
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, err
	}

	if !hasGlobMetacharacter(pattern) {
		if _, err := remote.Lstat(pattern); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return []string{}, nil
			}
			return nil, err
		}
		return []string{pattern}, nil
	}

	dir, base := path.Split(pattern)
	dir = cleanGlobDir(dir)

	if !hasGlobMetacharacter(dir) {
		return globInDir(remote, dir, base, []string{})
	}

	// Protects against infinite recursion: a pattern that reduces to itself
	// (which a loose backslash can produce) would never converge.
	if dir == pattern {
		return nil, path.ErrBadPattern
	}

	dirs, err := remoteGlob(remote, dir)
	if err != nil {
		return nil, err
	}

	matches := []string{}
	for _, d := range dirs {
		matches, err = globInDir(remote, d, base, matches)
		if err != nil {
			return nil, err
		}
	}
	return matches, nil
}

// globInDir appends to matches the entries of dir that match the
// pattern.
//
// The sorting is deliberate: the SFTP ReadDir hands back whatever the server
// sends, in whatever order it likes, and the list of include files feeds the
// canonical hash of the configuration. An unstable order would become an
// unstable hash.
func globInDir(remote remoteReader, dir, pattern string, matches []string) ([]string, error) {
	entries, err := remote.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A nonexistent directory is absence of matches, not a
			// failure: the pattern simply matches nothing.
			return matches, nil
		}
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		matched, err := path.Match(pattern, name)
		if err != nil {
			return nil, err
		}
		if matched {
			matches = append(matches, path.Join(dir, name))
		}
	}
	return matches, nil
}

func hasGlobMetacharacter(p string) bool {
	return strings.ContainsAny(p, globMetacharacters)
}

// cleanGlobDir normalizes the directory half returned by path.Split, which
// always comes with a trailing slash. A relative pattern has an empty
// directory and becomes ".", which is what ReadDir expects.
func cleanGlobDir(dir string) string {
	switch dir {
	case "":
		return "."
	case "/":
		return "/"
	default:
		return strings.TrimSuffix(dir, "/")
	}
}

// sshConnectionError describes the handshake failure naming the authentication
// methods that were offered.
//
// The list of methods is what separates "the network does not reach it" from
// "the network reaches it and the server accepted nothing I had". Without it,
// a refusal caused by a missing key in the ssh-agent is indistinguishable from
// a host that is down, and whoever reads the output has no way of choosing
// what to fix.
func sshConnectionError(user, address string, methods []string, cause error) error {
	offered := "none"
	if len(methods) > 0 {
		offered = strings.Join(methods, ", ")
	}

	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoConexaoSSH,
			Message: fmt.Sprintf(
				"could not connect to %s@%s: %v. Authentication methods "+
					"offered: %s",
				user, address, cause, offered),
		},
		Err: cause,
	}
}

// sftpSessionError describes the case where SSH comes up and SFTP does not. The
// distinction matters because the fix lives in the server's sshd, not in the
// caller's credentials.
func sftpSessionError(user, address string, cause error) error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoSessaoSFTP,
			Message: fmt.Sprintf(
				"the connection to %s@%s was established, but the SFTP subsystem did "+
					"not answer: %v. ngx reads the configuration over SFTP; check whether "+
					"the server's sshd has the subsystem enabled",
				user, address, cause),
		},
		Err: cause,
	}
}

// recordedKeyTypes returns the key types known_hosts has for the host, and
// only when the failure was "it presented a type that is not on record". It
// returns nothing for a genuinely changed key or an unknown host: in those
// cases repeating the handshake would be bypassing the verification, not
// adjusting it.
func recordedKeyTypes(err error) []string {
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) == 0 {
		return nil
	}

	types := make([]string, 0, len(keyErr.Want))
	seen := map[string]bool{}
	for i := range keyErr.Want {
		t := keyErr.Want[i].Key.Type()
		if !seen[t] {
			seen[t] = true
			types = append(types, t)
		}
	}
	return types
}
