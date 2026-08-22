//go:build integration

package transport

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Privileged writing against a real sudo, a real non-root user, and a real
// root-owned file.
//
// The unit tests assert the SEQUENCE of commands with a recorder, which is what
// they are for: they prove the mode is set before the content exists and that
// the rename comes last. They cannot prove that sudo permits any of it, that
// `install -o 0` is accepted, or that the resulting file is really owned by
// root -- and those are the properties an operator depends on.
const writeBenchContainer = "ngx-bench"

// benchRunner runs each argv inside the bench container AS THE UNPRIVILEGED
// USER, which is the whole point: a test that ran as root would exercise a path
// where sudo is a no-op.
type benchRunner struct{}

func (benchRunner) Open(string) (io.ReadCloser, error) { return nil, errors.New("unused") }
func (benchRunner) Glob(string) ([]string, error)      { return nil, errors.New("unused") }
func (benchRunner) Close() error                       { return nil }
func (benchRunner) Describe() string                   { return "docker://" + writeBenchContainer }

func (b benchRunner) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	return b.RunWithInput(ctx, argv, nil)
}

func (benchRunner) RunWithInput(ctx context.Context, argv []string, stdin []byte) ([]byte, []byte, int, error) {
	full := append([]string{"exec", "-i", "-u", "ngxtest", writeBenchContainer}, argv...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}

	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil, out, ee.ExitCode(), nil
	}
	if err != nil {
		return nil, out, 0, err
	}
	return nil, out, 0, nil
}

func requireWriteBench(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available; bring the bench up to run this")
	}
	if err := exec.Command("docker", "inspect", writeBenchContainer).Run(); err != nil {
		t.Skip("the bench is not up: run `make bench-up`")
	}
}

func inBench(t *testing.T, asUser string, args ...string) (string, int) {
	t.Helper()
	full := append([]string{"exec", "-u", asUser, writeBenchContainer}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	return string(out), code
}

// The premise every other test here rests on: the unprivileged user really
// cannot write the file. Without this, a passing write would prove nothing
// about privilege.
func TestTheBenchUserCannotWriteTheTargetWithoutPrivilege(t *testing.T) {
	requireWriteBench(t)

	const target = "/etc/nginx/conf.d/00-default.conf"
	_, code := inBench(t, "ngxtest", "sh", "-c", "echo x >> "+target)
	require.NotZero(t, code, "the bench user can write %s, so nothing here tests privilege", target)
}

func TestARealPrivilegedWriteReplacesARootOwnedFile(t *testing.T) {
	requireWriteBench(t)

	const target = "/etc/nginx/conf.d/00-default.conf"
	before, _ := inBench(t, "root", "cat", target)
	require.NotEmpty(t, before)
	t.Cleanup(func() {
		w, err := NewPrivilegedWriter(benchRunner{})
		require.NoError(t, err)
		require.NoError(t, w.WriteFile(context.Background(), target, []byte(before), 0o644, 0, 0))
	})

	w, err := NewPrivilegedWriter(benchRunner{})
	require.NoError(t, err)
	require.NoError(t, w.Available(context.Background()),
		"sudo -n is not usable in the bench, so this test cannot run")

	content := "# written by ngx with privilege\nserver { listen 8099; }\n"
	require.NoError(t, w.WriteFile(context.Background(), target, []byte(content), 0o640, 0, 0))

	got, _ := inBench(t, "root", "cat", target)
	require.Equal(t, content, got)

	// Owner and mode are what was asked for, read back from the filesystem
	// rather than assumed from the arguments.
	owner, _ := inBench(t, "root", "stat", "-c", "%U:%G %a", target)
	require.Equal(t, "root:root 640", strings.TrimSpace(owner))

	// And nothing was left behind in a directory an operator will look at.
	listing, _ := inBench(t, "root", "ls", "-a", "/etc/nginx/conf.d")
	require.NotContains(t, listing, ".ngx-apply-",
		"a temporary file survived a successful privileged write")
}

// A privileged create of a file that did not exist, then its removal, both with
// real sudo.
func TestARealPrivilegedCreateAndRemove(t *testing.T) {
	requireWriteBench(t)

	const target = "/etc/nginx/conf.d/zz-ngx-privileged.conf"
	t.Cleanup(func() { inBench(t, "root", "rm", "-f", target) })

	w, err := NewPrivilegedWriter(benchRunner{})
	require.NoError(t, err)

	require.NoError(t, w.WriteFile(context.Background(), target,
		[]byte("server { listen 8098; }\n"), 0o644, 0, 0))

	out, code := inBench(t, "root", "test", "-f", target)
	require.Zero(t, code, "the file was not created: %s", out)

	require.NoError(t, w.Remove(context.Background(), target))
	_, code = inBench(t, "root", "test", "-f", target)
	require.NotZero(t, code, "the file survived a privileged removal")
}

// The failure that matters operationally: sudo refuses. It has to come back as
// an error naming what was refused, not as a silent success or a hang.
//
// It is provoked by asking for a command the bench's sudoers does not permit --
// which is also a check on the sudoers itself, since the rule is deliberately a
// list rather than ALL.
func TestARefusedSudoIsAnErrorAndNotAHang(t *testing.T) {
	requireWriteBench(t)

	w, err := NewPrivilegedWriter(benchRunner{})
	require.NoError(t, err)

	// chown is NOT in the bench's sudoers list.
	err = w.sudo(context.Background(), nil, "chown", "ngxtest", "/etc/nginx/nginx.conf")
	require.Error(t, err, "the bench's sudoers permits chown, so the rule is wider than intended")
	require.Contains(t, err.Error(), "chown", "the refusal has to name what was refused")
}
