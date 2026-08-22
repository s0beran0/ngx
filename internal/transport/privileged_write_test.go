package transport

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingRunner records every argv and the stdin it was given, so the
// SEQUENCE can be asserted rather than only the outcome.
//
// The sequence is the design here: which command runs first decides whether a
// 0600 configuration is briefly world-readable, and whether the content is on
// disk before the rename. An outcome test would pass for a version that got
// both wrong.
type recordingRunner struct {
	calls  [][]string
	stdins [][]byte
	fail   map[string]int // argv[2] (the command) -> exit code
	err    error
}

func (r *recordingRunner) Open(string) (io.ReadCloser, error) { return nil, errors.New("unused") }
func (r *recordingRunner) Glob(string) ([]string, error)      { return nil, errors.New("unused") }
func (r *recordingRunner) Close() error                       { return nil }
func (r *recordingRunner) Describe() string                   { return "recording" }

func (r *recordingRunner) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	return r.RunWithInput(ctx, argv, nil)
}

func (r *recordingRunner) RunWithInput(_ context.Context, argv []string, stdin []byte) ([]byte, []byte, int, error) {
	r.calls = append(r.calls, append([]string(nil), argv...))
	r.stdins = append(r.stdins, append([]byte(nil), stdin...))
	if r.err != nil {
		return nil, nil, 0, r.err
	}
	if len(argv) > 2 {
		if code, ok := r.fail[argv[2]]; ok {
			return nil, []byte("refused"), code, nil
		}
	}
	return nil, nil, 0, nil
}

// The ordering, asserted step by step, with the reason each step is where it is.
func TestAPrivilegedWriteCreatesWithTheFinalModeThenFillsThenRenames(t *testing.T) {
	rec := &recordingRunner{}
	w := &PrivilegedWriter{tr: rec}

	require.NoError(t, w.WriteFile(context.Background(),
		"/etc/nginx/conf.d/site.conf", []byte("server {}\n"), 0o600, 0, 0))

	require.Len(t, rec.calls, 3)

	// 1. install, with the FINAL mode and owner, on an empty file. Fixing the
	//    mode afterwards would leave a window where a 0600 configuration is
	//    readable by anyone who can reach the directory.
	install := rec.calls[0]
	require.Equal(t, []string{"sudo", "-n", "install"}, install[:3])
	require.Contains(t, install, "-m")
	require.Contains(t, install, "0600")
	require.Contains(t, install, "/dev/null")
	require.Equal(t, "/etc/nginx/conf.d", parentOf(install[len(install)-1]),
		"the temporary file is not in the target's directory, so the rename could "+
			"cross a filesystem and stop being atomic")

	// 2. dd with conv=fsync, content on stdin. `tee` would not fsync, and a
	//    crash between write and rename would leave a file of the right size
	//    and null content.
	write := rec.calls[1]
	require.Equal(t, []string{"sudo", "-n", "dd"}, write[:3])
	require.Contains(t, write, "conv=fsync")
	require.Equal(t, "server {}\n", string(rec.stdins[1]))

	// 3. mv, within the same directory: a rename, atomic.
	move := rec.calls[2]
	require.Equal(t, []string{"sudo", "-n", "mv", "-f"}, move[:4])
	require.Equal(t, "/etc/nginx/conf.d/site.conf", move[len(move)-1])

	// The same temporary file throughout, or the three steps are not about one
	// file at all.
	tmp := install[len(install)-1]
	require.Equal(t, "of="+tmp, write[3])
	require.Equal(t, tmp, move[4])
}

// Every sudo is -n. A prompt nobody can answer would hang an agent forever, and
// this is the assertion that keeps a future edit from dropping the flag.
func TestEverySudoIsNonInteractive(t *testing.T) {
	rec := &recordingRunner{}
	w := &PrivilegedWriter{tr: rec}

	require.NoError(t, w.WriteFile(context.Background(), "/etc/nginx/a.conf", []byte("x"), 0o644, 0, 0))
	require.NoError(t, w.Remove(context.Background(), "/etc/nginx/a.conf"))
	require.NoError(t, w.Available(context.Background()))

	require.NotEmpty(t, rec.calls)
	for _, argv := range rec.calls {
		require.Equal(t, "sudo", argv[0])
		require.Equal(t, "-n", argv[1], "a sudo without -n can wait for a password: %v", argv)
	}
}

// A failure at any step removes the temporary file. A directory of
// .ngx-apply-* files in /etc/nginx is how an operator discovers this code the
// hard way.
func TestAFailedPrivilegedWriteRemovesItsTemporaryFile(t *testing.T) {
	for _, failing := range []string{"dd", "mv"} {
		t.Run("at "+failing, func(t *testing.T) {
			rec := &recordingRunner{fail: map[string]int{failing: 1}}
			w := &PrivilegedWriter{tr: rec}

			err := w.WriteFile(context.Background(), "/etc/nginx/a.conf", []byte("x"), 0o644, 0, 0)
			require.Error(t, err)

			last := rec.calls[len(rec.calls)-1]
			require.Equal(t, []string{"sudo", "-n", "rm", "-f", "--"}, last[:5],
				"the temporary file was not cleaned up after a failure at %s", failing)
		})
	}
}

// The transport that cannot feed stdin says so at construction, by name, rather
// than failing somewhere deeper. That is how "remote privileged writing is not
// in this version" is expressed.
func TestATransportWithoutStdinIsRefusedUpFront(t *testing.T) {
	_, err := NewPrivilegedWriter(plainTransport{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "standard input")
	require.Contains(t, err.Error(), "plain", "the refusal has to name the transport")
}

// The local transport does implement it, which is what makes local privileged
// writing available at all.
func TestTheLocalTransportCanFeedStdin(t *testing.T) {
	tr := Local()
	defer tr.Close()

	_, err := NewPrivilegedWriter(tr)
	require.NoError(t, err)
}

// plainTransport is a Transport and deliberately NOT an InputRunner.
type plainTransport struct{}

func (plainTransport) Open(string) (io.ReadCloser, error) { return nil, errors.New("unused") }
func (plainTransport) Glob(string) ([]string, error)      { return nil, errors.New("unused") }
func (plainTransport) Run(context.Context, []string) ([]byte, []byte, int, error) {
	return nil, nil, 0, nil
}
func (plainTransport) Close() error     { return nil }
func (plainTransport) Describe() string { return "plain" }

func parentOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "."
}

var _ = os.FileMode(0)
