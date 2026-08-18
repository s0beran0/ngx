package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ---------------------------------------------------------------------------
// Remote glob (DR6) — the part of this layer that matters most.
// ---------------------------------------------------------------------------

// fakeEntry is the minimum os.FileInfo the glob consumes: just the name.
type fakeEntry struct {
	name string
	dir  bool
}

func (e fakeEntry) Name() string       { return e.name }
func (e fakeEntry) Size() int64        { return 0 }
func (e fakeEntry) Mode() os.FileMode  { return 0o644 }
func (e fakeEntry) ModTime() time.Time { return time.Time{} }
func (e fakeEntry) IsDir() bool        { return e.dir }
func (e fakeEntry) Sys() any           { return nil }

// fakeRemote is an in-memory tree with per-path injectable failures. It is
// the only way to exercise the case DR6 exists to cover: an I/O error in the
// middle of the listing, which a healthy test server never produces.
type fakeRemote struct {
	// dirs maps a directory path to the names it contains.
	dirs map[string][]string
	// files is the set of paths that exist for Lstat.
	files map[string]bool
	// failures maps a path to the error ReadDir/Lstat returns there.
	failures map[string]error

	calls []string
}

func (r *fakeRemote) ReadDir(p string) ([]os.FileInfo, error) {
	r.calls = append(r.calls, "ReadDir:"+p)
	if err, ok := r.failures[p]; ok {
		return nil, err
	}
	names, ok := r.dirs[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	entries := make([]os.FileInfo, 0, len(names))
	for _, n := range names {
		_, isDir := r.dirs[path.Join(p, n)]
		entries = append(entries, fakeEntry{name: n, dir: isDir})
	}
	return entries, nil
}

func (r *fakeRemote) Lstat(p string) (os.FileInfo, error) {
	r.calls = append(r.calls, "Lstat:"+p)
	if err, ok := r.failures[p]; ok {
		return nil, err
	}
	if r.files[p] {
		return fakeEntry{name: path.Base(p)}, nil
	}
	if _, ok := r.dirs[p]; ok {
		return fakeEntry{name: path.Base(p), dir: true}, nil
	}
	return nil, fs.ErrNotExist
}

func nginxRemote() *fakeRemote {
	return &fakeRemote{
		dirs: map[string][]string{
			// Shuffled on purpose: the server hands back whatever it
			// likes and the glob has to return a sorted result.
			"/etc/nginx":            {"conf.d", "sites", "nginx.conf", "mime.types"},
			"/etc/nginx/conf.d":     {"zz-ultimo.conf", "gzip.conf", "aa-primeiro.conf", "leiame.txt"},
			"/etc/nginx/sites":      {"a", "b"},
			"/etc/nginx/sites/a":    {"srv.conf"},
			"/etc/nginx/sites/b":    {"srv.conf", "extra.conf"},
			".":                     {"local.conf", "outro.txt"},
			"/etc/nginx/conf.d/sub": {},
		},
		files: map[string]bool{
			"/etc/nginx/nginx.conf":            true,
			"/etc/nginx/conf.d/gzip.conf":      true,
			"/etc/nginx/conf.d/zz-ultimo.conf": true,
		},
		failures: map[string]error{},
	}
}

func TestRemoteGlobExpandsAndSorts(t *testing.T) {
	r := nginxRemote()

	matches, err := remoteGlob(r, "/etc/nginx/conf.d/*.conf")

	require.NoError(t, err)
	assert.Equal(t, []string{
		"/etc/nginx/conf.d/aa-primeiro.conf",
		"/etc/nginx/conf.d/gzip.conf",
		"/etc/nginx/conf.d/zz-ultimo.conf",
	}, matches, "the order feeds the canonical hash: it has to be stable")
}

func TestRemoteGlobPropagatesIOErrorWhileListing(t *testing.T) {
	// This is the test DR6 exists to make possible. sftp.Client.Glob would
	// return an empty list and a nil err here, and ngx would present the
	// server configuration without the files it actually has.
	r := nginxRemote()
	linkDown := errors.New("connection lost")
	r.failures["/etc/nginx/conf.d"] = linkDown

	matches, err := remoteGlob(r, "/etc/nginx/conf.d/*.conf")

	require.Error(t, err, "an I/O error cannot become an empty list")
	assert.ErrorIs(t, err, linkDown)
	assert.Nil(t, matches)
}

func TestRemoteGlobPropagatesPermissionDenied(t *testing.T) {
	// "I could not read" is not "it does not exist": only absence becomes
	// an empty list.
	r := nginxRemote()
	r.failures["/etc/nginx/conf.d"] = fs.ErrPermission

	_, err := remoteGlob(r, "/etc/nginx/conf.d/*.conf")

	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrPermission)
}

func TestRemoteGlobMissingDirIsNotAnError(t *testing.T) {
	r := nginxRemote()

	matches, err := remoteGlob(r, "/etc/nginx/nao-existe/*.conf")

	require.NoError(t, err)
	assert.Equal(t, []string{}, matches)
	assert.NotNil(t, matches, "empty list, never nil")
}

func TestRemoteGlobWithoutMetacharacterFilePresent(t *testing.T) {
	r := nginxRemote()

	matches, err := remoteGlob(r, "/etc/nginx/nginx.conf")

	require.NoError(t, err)
	assert.Equal(t, []string{"/etc/nginx/nginx.conf"}, matches)
	assert.Contains(t, r.calls, "Lstat:/etc/nginx/nginx.conf")
}

func TestRemoteGlobWithoutMetacharacterFileMissing(t *testing.T) {
	r := nginxRemote()

	matches, err := remoteGlob(r, "/etc/nginx/sumiu.conf")

	require.NoError(t, err)
	assert.Equal(t, []string{}, matches)
}

func TestRemoteGlobWithoutMetacharacterPropagatesIOError(t *testing.T) {
	// The literal path in sftp.Client.Glob is `if err != nil { return nil, nil }`.
	// Here it has to return an error.
	r := nginxRemote()
	linkDown := errors.New("connection lost")
	r.failures["/etc/nginx/nginx.conf"] = linkDown

	matches, err := remoteGlob(r, "/etc/nginx/nginx.conf")

	require.Error(t, err)
	assert.ErrorIs(t, err, linkDown)
	assert.Nil(t, matches)
}

func TestRemoteGlobMetacharacterInDir(t *testing.T) {
	r := nginxRemote()

	matches, err := remoteGlob(r, "/etc/nginx/sites/*/*.conf")

	require.NoError(t, err)
	assert.Equal(t, []string{
		"/etc/nginx/sites/a/srv.conf",
		"/etc/nginx/sites/b/extra.conf",
		"/etc/nginx/sites/b/srv.conf",
	}, matches)
}

func TestRemoteGlobPropagatesErrorWhileExpandingDir(t *testing.T) {
	r := nginxRemote()
	linkDown := errors.New("connection lost")
	r.failures["/etc/nginx/sites/b"] = linkDown

	_, err := remoteGlob(r, "/etc/nginx/sites/*/*.conf")

	require.Error(t, err, "a failure in a subdirectory cannot become a partial result")
	assert.ErrorIs(t, err, linkDown)
}

func TestRemoteGlobMalformedPattern(t *testing.T) {
	r := nginxRemote()

	_, err := remoteGlob(r, "/etc/nginx/[a-.conf")

	require.Error(t, err)
	assert.ErrorIs(t, err, path.ErrBadPattern)
	assert.Empty(t, r.calls, "an invalid pattern does not spend a network round trip")
}

func TestRemoteGlobRelativePatternListsCurrentDir(t *testing.T) {
	r := nginxRemote()

	matches, err := remoteGlob(r, "*.conf")

	require.NoError(t, err)
	assert.Equal(t, []string{"local.conf"}, matches)
	assert.Contains(t, r.calls, "ReadDir:.")
}

func TestRemoteGlobUsesPOSIXSeparator(t *testing.T) {
	// The guarantee that the glob never goes through filepath: if it did,
	// on a Windows client the result would come back with backslashes and
	// the Linux server would not find the file.
	r := nginxRemote()

	matches, err := remoteGlob(r, "/etc/nginx/sites/a/*.conf")

	require.NoError(t, err)
	require.Len(t, matches, 1)
	assert.Equal(t, "/etc/nginx/sites/a/srv.conf", matches[0])
	assert.NotContains(t, matches[0], `\`)
}

func TestRemoteGlobDoesNotRecurseForever(t *testing.T) {
	r := nginxRemote()

	_, err := remoteGlob(r, `\`)

	// What matters is that it terminates. The outcome is "no match" or
	// ErrBadPattern, never a loop.
	if err != nil {
		assert.ErrorIs(t, err, path.ErrBadPattern)
	}
}

// ---------------------------------------------------------------------------
// Distinction between exit code and transport error.
// ---------------------------------------------------------------------------

func TestClassifySSHExitCommandOK(t *testing.T) {
	stdout, stderr, code, err := classifySSHExit([]byte("ok"), nil, nil)

	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, []byte("ok"), stdout)
	assert.Nil(t, stderr)
}

func TestClassifySSHExitMissingIsTransportFailure(t *testing.T) {
	// The session ended without the server saying how: the connection
	// dropped. Returning code zero with a nil err would make that look like
	// success.
	_, _, code, err := classifySSHExit(nil, nil, &ssh.ExitMissingError{})

	require.Error(t, err)
	var missing *ssh.ExitMissingError
	assert.ErrorAs(t, err, &missing)
	assert.Equal(t, 0, code)
}

func TestClassifySSHExitIOErrorIsTransportFailure(t *testing.T) {
	_, _, _, err := classifySSHExit(nil, nil, io.ErrUnexpectedEOF)

	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

// ---------------------------------------------------------------------------
// Building the command line: zero shell.
// ---------------------------------------------------------------------------

func TestQuoteArgument(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"simple", "nginx", `'nginx'`},
		{"empty", "", `''`},
		{"space", "meu arquivo.conf", `'meu arquivo.conf'`},
		{"dollar", "$HOME/x", `'$HOME/x'`},
		{"backtick", "`id`", "'`id`'"},
		{"semicolon", "a; rm -rf /", `'a; rm -rf /'`},
		{"single quote", "o'brien", `'o'\''brien'`},
		{"backslash", `c:\x`, `'c:\x'`},
		{"newline", "a\nrm -rf /", "'a\nrm -rf /'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, quoteArgument(c.input))
		})
	}
}

func TestBuildCommandLineQuotesEachArgument(t *testing.T) {
	line := buildCommandLine([]string{"nginx", "-c", "/etc/nginx/nginx.conf"})

	assert.Equal(t, `'nginx' '-c' '/etc/nginx/nginx.conf'`, line)
}

// ---------------------------------------------------------------------------
// In-memory SSH server: the rest can only be proven end to end.
// ---------------------------------------------------------------------------

// execResponse is what the test server returns for a command.
type execResponse struct {
	stdout string
	stderr string
	code   uint32
	// noStatus reproduces the connection dropping: the channel closes with
	// no exit-status, and the client receives *ssh.ExitMissingError.
	noStatus bool
}

type testSSHServer struct {
	addr      string
	publicKey ssh.PublicKey
	listener  net.Listener
	wg        sync.WaitGroup
	mu        sync.Mutex
	received  []string
	respond   func(command string) execResponse
}

const testPassword = "senha-de-teste"

// startSSHServer brings up a real SSH server on 127.0.0.1:0, with an
// ephemeral host key and password authentication. Without it there is no way
// to prove that a non-zero exit code arrives as a result and a dropped
// connection arrives as an error.
func startSSHServer(t *testing.T, respond func(string) execResponse) *testSSHServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) == testPassword {
				return nil, nil
			}
			return nil, fmt.Errorf("invalid password")
		},
	}
	cfg.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := &testSSHServer{
		addr:      listener.Addr().String(),
		publicKey: signer.PublicKey(),
		listener:  listener,
		respond:   respond,
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(conn, cfg)
			}()
		}
	}()

	t.Cleanup(func() {
		_ = listener.Close()
		s.wg.Wait()
	})
	return s
}

func (s *testSSHServer) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	defer func() { _ = conn.Close() }()

	sc, newChannels, globalRequests, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sc.Close() }()
	go ssh.DiscardRequests(globalRequests)

	for newChan := range newChannels {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, requests, err := newChan.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveSession(channel, requests)
		}()
	}
}

func (s *testSSHServer) serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	closeChannel := true
	defer func() {
		if closeChannel {
			_ = channel.Close()
		}
	}()

	for req := range requests {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				_ = req.Reply(false, nil)
				return
			}
			_ = req.Reply(true, nil)

			s.mu.Lock()
			s.received = append(s.received, payload.Command)
			s.mu.Unlock()

			resp := s.respond(payload.Command)
			_, _ = io.WriteString(channel, resp.stdout)
			_, _ = io.WriteString(channel.Stderr(), resp.stderr)
			if !resp.noStatus {
				_, _ = channel.SendRequest("exit-status", false,
					ssh.Marshal(struct{ Status uint32 }{resp.code}))
			}
			return

		case "subsystem":
			var payload struct{ Name string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil || payload.Name != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			srv, err := sftp.NewServer(channel)
			if err != nil {
				return
			}
			closeChannel = false
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer func() { _ = srv.Close() }()
				_ = srv.Serve()
			}()

		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (s *testSSHServer) receivedCommands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.received...)
}

// optionsFor builds the SSHOptions that reach the test server, writing the
// host key into a temporary known_hosts. Strict verification, as in
// production: the test also proves the DR1 policy does not get in the way of
// the legitimate path.
func optionsFor(t *testing.T, s *testSSHServer) SSHOptions {
	t.Helper()

	host, portText, err := net.SplitHostPort(s.addr)
	require.NoError(t, err)
	port := 0
	_, err = fmt.Sscanf(portText, "%d", &port)
	require.NoError(t, err)

	line := knownhosts.Line([]string{knownhosts.Normalize(s.addr)}, s.publicKey)
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(knownHostsPath, []byte(line+"\n"), 0o600))

	return SSHOptions{
		Host:           host,
		Port:           port,
		User:           "operador",
		Password:       testPassword,
		KnownHostsPath: knownHostsPath,
		Timeout:        10 * time.Second,
	}
}

func TestSSHRunNonZeroCodeIsNotAnError(t *testing.T) {
	// The central Transport invariant: `nginx -t` rejecting the
	// configuration is a result, not an infrastructure failure.
	s := startSSHServer(t, func(string) execResponse {
		return execResponse{
			stderr: "nginx: configuration file test failed\n",
			code:   1,
		}
	})

	tr := connect(t, s)

	stdout, stderr, code, err := tr.Run(context.Background(), []string{"nginx", "-t"})

	require.NoError(t, err, "a non-zero exit code is a result, never an error")
	assert.Equal(t, 1, code)
	assert.Empty(t, stdout)
	assert.Contains(t, string(stderr), "test failed")
}

func TestSSHRunSuccess(t *testing.T) {
	s := startSSHServer(t, func(string) execResponse {
		return execResponse{stdout: "nginx version: 1.20.1\n"}
	})
	tr := connect(t, s)

	stdout, _, code, err := tr.Run(context.Background(), []string{"nginx", "-v"})

	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Contains(t, string(stdout), "1.20.1")
}

func TestSSHRunDroppedConnectionIsTransportError(t *testing.T) {
	// The channel closes with no exit-status. Without the distinction, this
	// would become "code 0, err nil" — an interrupted command passing for
	// success.
	s := startSSHServer(t, func(string) execResponse {
		return execResponse{stdout: "parcial", noStatus: true}
	})
	tr := connect(t, s)

	_, _, code, err := tr.Run(context.Background(), []string{"nginx", "-T"})

	require.Error(t, err, "a session with no exit status is a transport failure")
	var missing *ssh.ExitMissingError
	assert.ErrorAs(t, err, &missing)
	assert.Equal(t, 0, code)
}

func TestSSHRunQuotesEachArgument(t *testing.T) {
	s := startSSHServer(t, func(string) execResponse { return execResponse{} })
	tr := connect(t, s)

	_, _, _, err := tr.Run(context.Background(),
		[]string{"nginx", "-c", "/etc/nginx/a b; rm -rf /"})

	require.NoError(t, err)
	received := s.receivedCommands()
	require.Len(t, received, 1)
	assert.Equal(t, `'nginx' '-c' '/etc/nginx/a b; rm -rf /'`, received[0])
	assert.NotContains(t, received[0], "nginx -c /etc")
}

func TestSSHRunEmptyArgv(t *testing.T) {
	s := startSSHServer(t, func(string) execResponse { return execResponse{} })
	tr := connect(t, s)

	_, _, _, err := tr.Run(context.Background(), nil)

	require.Error(t, err)
}

func TestSSHRunCanceledContext(t *testing.T) {
	release := make(chan struct{})
	s := startSSHServer(t, func(string) execResponse {
		<-release
		return execResponse{}
	})
	t.Cleanup(func() { close(release) })

	tr := connect(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, _, code, err := tr.Run(ctx, []string{"nginx", "-T"})

	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, code)
}

func TestSSHRunAlreadyCanceledContext(t *testing.T) {
	s := startSSHServer(t, func(string) execResponse { return execResponse{} })
	tr := connect(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, err := tr.Run(ctx, []string{"nginx", "-t"})

	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, s.receivedCommands(), "a dead context does not spend a session")
}

func TestSSHDescribe(t *testing.T) {
	s := startSSHServer(t, func(string) execResponse { return execResponse{} })
	opts := optionsFor(t, s)

	tr, _, err := SSHComDiagnosticos(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tr.Close() })

	assert.Equal(t, fmt.Sprintf("ssh://operador@%s:%d", opts.Host, opts.Port), tr.Describe())
	assert.True(t, strings.HasPrefix(tr.Describe(), "ssh://"))
}

func TestSSHCloseTwiceIsSafe(t *testing.T) {
	s := startSSHServer(t, func(string) execResponse { return execResponse{} })
	tr := connect(t, s)

	require.NoError(t, tr.Close())
	assert.NotPanics(t, func() { _ = tr.Close() })
}

func TestSSHUnknownHostKeyIsRefused(t *testing.T) {
	s := startSSHServer(t, func(string) execResponse { return execResponse{} })
	opts := optionsFor(t, s)
	// Empty known_hosts: the host becomes unknown.
	require.NoError(t, os.WriteFile(opts.KnownHostsPath, []byte("\n"), 0o600))

	tr, _, err := SSHComDiagnosticos(opts)

	require.Error(t, err)
	assert.Nil(t, tr)
	assert.Contains(t, err.Error(), "unknown host")

	// The code has to reach the caller intact: it is by the code, and not
	// by the text, that the consumer separates first access from a changed
	// key (DR1). Wrapping this in CodigoConexaoSSH would make a
	// verification refusal look like a network or credential failure.
	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodigoHostDesconhecido, e.Diag.Code)
}

func TestSSHRefusedConnectionHasActionableDiagnostic(t *testing.T) {
	s := startSSHServer(t, func(string) execResponse { return execResponse{} })
	opts := optionsFor(t, s)
	opts.Password = "errada"

	tr, diags, err := SSHComDiagnosticos(opts)

	require.Error(t, err)
	assert.Nil(t, tr)
	assert.NotNil(t, diags)
	assert.Contains(t, err.Error(), "could not connect")
	assert.Contains(t, err.Error(), "Authentication methods offered")
}

func TestSSHWithoutHostIsUsageError(t *testing.T) {
	tr, diags, err := SSHComDiagnosticos(SSHOptions{Host: "  "})

	require.Error(t, err)
	assert.Nil(t, tr)
	assert.NotNil(t, diags, "the list of diagnostics is never nil")
}

// ---------------------------------------------------------------------------
// SFTP end to end: Open and Glob against a real server.
// ---------------------------------------------------------------------------

func TestSSHOpenAndGlobEndToEnd(t *testing.T) {
	s := startSSHServer(t, func(string) execResponse { return execResponse{} })
	tr := connect(t, s)

	root := t.TempDir()
	confd := filepath.Join(root, "conf.d")
	require.NoError(t, os.MkdirAll(confd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "nginx.conf"),
		[]byte("include conf.d/*.conf;\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(confd, "gzip.conf"), []byte("gzip on;\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(confd, "ssl.conf"), []byte("ssl on;\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(confd, "leiame.txt"), []byte("nao\n"), 0o644))

	// A remote path is POSIX: filepath.ToSlash covers a Windows client.
	rootPOSIX := filepath.ToSlash(root)

	rc, err := tr.Open(rootPOSIX + "/nginx.conf")
	require.NoError(t, err)
	content, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	assert.Equal(t, "include conf.d/*.conf;\n", string(content))

	matches, err := tr.Glob(rootPOSIX + "/conf.d/*.conf")
	require.NoError(t, err)
	assert.Equal(t, []string{
		rootPOSIX + "/conf.d/gzip.conf",
		rootPOSIX + "/conf.d/ssl.conf",
	}, matches)

	empty, err := tr.Glob(rootPOSIX + "/nao-existe/*.conf")
	require.NoError(t, err)
	assert.Equal(t, []string{}, empty)

	_, err = tr.Open(rootPOSIX + "/sumiu.conf")
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestSSHGlobPropagatesErrorAfterConnectionDrops(t *testing.T) {
	// The opposite of sftp.Client.Glob: with the connection dead it would
	// return (nil, nil) on the path with no metacharacters and an empty
	// list on the other one.
	s := startSSHServer(t, func(string) execResponse { return execResponse{} })
	tr := connect(t, s)

	root := filepath.ToSlash(t.TempDir())
	require.NoError(t, tr.Close())

	_, err := tr.Glob(root + "/*.conf")
	require.Error(t, err, "a dead connection cannot become an empty list")

	_, err = tr.Glob(root + "/nginx.conf")
	require.Error(t, err, "a dead connection cannot become an empty list")
}

func connect(t *testing.T, s *testSSHServer) Transport {
	t.Helper()
	tr, diags, err := SSHComDiagnosticos(optionsFor(t, s))
	require.NoError(t, err)
	require.NotNil(t, diags)
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}
