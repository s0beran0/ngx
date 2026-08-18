package transport

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelperProcess is not a test: it is the helper program the Run tests
// execute. Re-executing the test binary itself avoids depending on system
// utilities, which vary between Linux, macOS and Windows, and requires no
// shell.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("NGX_TRANSPORT_HELPER") != "1" {
		return
	}
	if out := os.Getenv("NGX_TRANSPORT_HELPER_STDOUT"); out != "" {
		fmt.Fprint(os.Stdout, out)
	}
	if errOut := os.Getenv("NGX_TRANSPORT_HELPER_STDERR"); errOut != "" {
		fmt.Fprint(os.Stderr, errOut)
	}
	code, _ := strconv.Atoi(os.Getenv("NGX_TRANSPORT_HELPER_EXIT"))
	os.Exit(code)
}

// helperArgv builds the argv that re-executes this test binary in helper mode,
// exiting with the requested code.
func helperArgv(t *testing.T, exitCode int, stdout, stderr string) []string {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("NGX_TRANSPORT_HELPER", "1")
	t.Setenv("NGX_TRANSPORT_HELPER_EXIT", strconv.Itoa(exitCode))
	t.Setenv("NGX_TRANSPORT_HELPER_STDOUT", stdout)
	t.Setenv("NGX_TRANSPORT_HELPER_STDERR", stderr)
	return []string{self, "-test.run=^TestHelperProcess$"}
}

func TestLocalOpenExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(path, []byte("worker_processes 1;\n"), 0o600))

	tr := Local()
	defer tr.Close()

	f, err := tr.Open(path)
	require.NoError(t, err)
	defer f.Close()

	content, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, "worker_processes 1;\n", string(content))
}

func TestLocalOpenMissingFile(t *testing.T) {
	tr := Local()
	defer tr.Close()

	f, err := tr.Open(filepath.Join(t.TempDir(), "does-not-exist.conf"))
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err), "the error should be a missing-file one, got %v", err)
	assert.Nil(t, f)
}

func TestLocalGlobMatches(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.conf"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.conf"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.txt"), nil, 0o600))

	tr := Local()
	defer tr.Close()

	matches, err := tr.Glob(filepath.Join(dir, "*.conf"))
	require.NoError(t, err)
	assert.Equal(t, []string{
		filepath.Join(dir, "a.conf"),
		filepath.Join(dir, "b.conf"),
	}, matches)
}

func TestLocalGlobNoMatches(t *testing.T) {
	tr := Local()
	defer tr.Close()

	matches, err := tr.Glob(filepath.Join(t.TempDir(), "*.conf"))
	require.NoError(t, err)
	// Empty list, never nil: a nil list would become "null" in the JSON.
	assert.NotNil(t, matches)
	assert.Empty(t, matches)
}

func TestLocalRunExitZero(t *testing.T) {
	argv := helperArgv(t, 0, "all good", "")

	tr := Local()
	defer tr.Close()

	stdout, stderr, exitCode, err := tr.Run(context.Background(), argv)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "all good", string(stdout))
	assert.Empty(t, string(stderr))
}

// TestLocalRunNonZeroExit is the test that prevents the inversion: a
// non-zero exit code is the command's result, with a nil err. If somebody
// turns that into an error, this test fails.
func TestLocalRunNonZeroExit(t *testing.T) {
	argv := helperArgv(t, 3, "", "nginx: configuration file test failed")

	tr := Local()
	defer tr.Close()

	stdout, stderr, exitCode, err := tr.Run(context.Background(), argv)
	require.NoError(t, err, "a non-zero exit code is a result, not a transport error")
	assert.Equal(t, 3, exitCode)
	assert.Empty(t, string(stdout))
	assert.Equal(t, "nginx: configuration file test failed", string(stderr))
}

// TestLocalRunMissingBinary is the other half of the distinction: here
// there was no command at all, so err has to be non-nil. If somebody turns a
// transport error into an exitCode, this test fails.
func TestLocalRunMissingBinary(t *testing.T) {
	tr := Local()
	defer tr.Close()

	argv := []string{filepath.Join(t.TempDir(), "binary-that-does-not-exist"), "-t"}
	_, _, _, err := tr.Run(context.Background(), argv)
	require.Error(t, err, "a missing binary is a transport error, not the command's verdict")
}

func TestLocalRunEmptyArgv(t *testing.T) {
	tr := Local()
	defer tr.Close()

	_, _, _, err := tr.Run(context.Background(), nil)
	require.Error(t, err)
}

// TestLocalRunCanceledContext makes sure cancellation becomes a transport
// error, and not just any exit code: a process killed by a signal also returns
// an ExitError.
func TestLocalRunCanceledContext(t *testing.T) {
	argv := helperArgv(t, 0, "", "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tr := Local()
	defer tr.Close()

	_, _, exitCode, err := tr.Run(ctx, argv)
	require.Error(t, err)
	assert.Equal(t, 0, exitCode)
}

func TestLocalDescribe(t *testing.T) {
	assert.Equal(t, "local", Local().Describe())
}

func TestLocalCloseIsIdempotent(t *testing.T) {
	tr := Local()
	require.NoError(t, tr.Close())
	require.NoError(t, tr.Close())
}

// Guards against an infinite suite runtime if the helper hangs.
func TestMain(m *testing.M) {
	done := make(chan int, 1)
	go func() { done <- m.Run() }()
	select {
	case code := <-done:
		os.Exit(code)
	case <-time.After(60 * time.Second):
		fmt.Fprintln(os.Stderr, "transport: suite exceeded 60s")
		os.Exit(1)
	}
}
