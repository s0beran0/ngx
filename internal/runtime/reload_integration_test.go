//go:build integration

package runtime_test

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/runtime"
)

// Reload against a real, RUNNING nginx.
//
// The unit tests prove the ordering and the reporting with a recorded transport,
// which is what they are for. They cannot prove that the signal reaches a master
// process and that it re-reads its configuration: that is a property of nginx,
// and it is the property the command exists for.
//
// So the transport here runs commands inside the bench container, which means
// runtime.Reload is exercised through its real code path -- argv, exit codes,
// output parsing -- against a binary that is actually serving.
const benchContainer = "ngx-bench-lua"

// containerTransport implements transport.Transport by running each argv through
// `docker exec`. Only Run is needed by Reload; the rest refuse loudly rather
// than returning something plausible, because a silent empty answer would make
// a broken test look like a passing one.
type containerTransport struct{}

func (containerTransport) Open(path string) (io.ReadCloser, error) {
	return nil, errors.New("containerTransport: Open is not used by Reload")
}

func (containerTransport) Glob(pattern string) ([]string, error) {
	return nil, errors.New("containerTransport: Glob is not used by Reload")
}

func (containerTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	full := append([]string{"exec", benchContainer}, argv...)
	cmd := exec.CommandContext(ctx, "docker", full...)

	out, err := cmd.CombinedOutput()
	exit := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		exit = ee.ExitCode()
		err = nil
	}
	// Everything on stderr, because that is where nginx writes and the runtime
	// reads the combined output anyway.
	return nil, out, exit, err
}

func (containerTransport) Close() error     { return nil }
func (containerTransport) Describe() string { return "docker://" + benchContainer }

func requireRunningNginx(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available; bring the bench up to run this")
	}
	if err := exec.Command("docker", "inspect", benchContainer).Run(); err != nil {
		t.Skip("the bench is not up: run `make bench-lua-up`")
	}
	if len(masterPids(t)) == 0 {
		t.Skip("no nginx master process in the bench, so a reload cannot be observed")
	}
}

// A reload keeps the master process and replaces the workers. That signature is
// what distinguishes a reload from a restart -- a restart would also make
// `-s reload` look like it worked, while dropping every connection, which is the
// opposite of what a reload is for.
func TestReloadAgainstARunningNginxReplacesTheWorkers(t *testing.T) {
	requireRunningNginx(t)

	masterBefore := masterPids(t)
	workersBefore := workerPids(t)
	require.NotEmpty(t, workersBefore, "no worker processes to compare against")

	// The binary in the bench is openresty, so the runtime is told which binary
	// to call rather than guessing.
	r := runtime.New(containerTransport{}, runtime.WithBinary("openresty"))

	res, err := r.Reload(context.Background())
	require.NoError(t, err)
	require.NotNil(t, res.Tested)
	require.True(t, res.Tested.OK, "the bench configuration is not valid, so this proves nothing:\n%s",
		res.Tested.Raw)
	require.True(t, res.Reloaded, "the reload did not happen:\n%s", res.Raw)

	// nginx replaces workers asynchronously, so this polls rather than sleeping
	// once and hoping.
	var workersAfter []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		workersAfter = workerPids(t)
		if len(workersAfter) > 0 && !sameSet(workersBefore, workersAfter) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	require.NotEmpty(t, workersAfter, "nginx has no workers after the reload")
	require.False(t, sameSet(workersBefore, workersAfter),
		"the workers did not change, so nothing was reloaded (before %v, after %v)",
		workersBefore, workersAfter)

	require.Equal(t, masterBefore, masterPids(t),
		"the master process changed, so this was a restart and not a reload")
}

// A configuration nginx refuses is never reloaded, proven against the real
// binary: the workers are the same afterwards, so no signal was sent.
func TestARealRefusalStopsTheReload(t *testing.T) {
	requireRunningNginx(t)

	workersBefore := workerPids(t)
	require.NotEmpty(t, workersBefore)

	// Break the configuration inside the container, and put it back whatever
	// happens. This is the one test here that writes, and it writes only to a
	// disposable container.
	conf := "/usr/local/openresty/nginx/conf/nginx.conf"
	original := runIn(t, "cat", conf)
	require.NotEmpty(t, original, "could not read the bench configuration")
	t.Cleanup(func() {
		writeInContainer(t, conf, original)
		_, _ = exec.Command("docker", "exec", benchContainer, "openresty", "-s", "reload").CombinedOutput()
	})
	writeInContainer(t, conf, original+"\nthis_is_not_a_directive;\n")

	r := runtime.New(containerTransport{}, runtime.WithBinary("openresty"))
	res, err := r.Reload(context.Background())

	// Not an error: nginx answered the question, and the answer was no.
	require.NoError(t, err)
	require.False(t, res.Tested.OK, "nginx accepted an invalid directive, so this proves nothing")
	require.False(t, res.Reloaded)

	require.True(t, sameSet(workersBefore, workerPids(t)),
		"the workers changed, so a reload happened against a configuration nginx had refused")
}

// --- helpers ---------------------------------------------------------------

func masterPids(t *testing.T) []string {
	t.Helper()
	return strings.Fields(runIn(t, "pgrep", "-f", "nginx: master"))
}

func workerPids(t *testing.T) []string {
	t.Helper()
	return strings.Fields(runIn(t, "pgrep", "-f", "nginx: worker"))
}

func runIn(t *testing.T, args ...string) string {
	t.Helper()
	full := append([]string{"exec", benchContainer}, args...)
	out, _ := exec.Command("docker", full...).CombinedOutput()
	return string(out)
}

func writeInContainer(t *testing.T, path, content string) {
	t.Helper()
	cmd := exec.Command("docker", "exec", "-i", benchContainer, "sh", "-c", "cat > "+path)
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "could not write %s in the container: %s", path, out)
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
		if seen[x] < 0 {
			return false
		}
	}
	return true
}
