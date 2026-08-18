package transport_test

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/transport"
)

// fake answers reads and executions from canned data, and records what got
// executed. Proving that something was NOT escalated means looking at the
// commands, not just at the result.
type fake struct {
	files    map[string]string
	denied   map[string]bool
	outputs  map[string]string
	failures map[string]bool
	executed [][]string
}

func newFake() *fake {
	return &fake{
		files: map[string]string{}, denied: map[string]bool{},
		outputs: map[string]string{}, failures: map[string]bool{},
	}
}

func (f *fake) Open(p string) (io.ReadCloser, error) {
	if f.denied[p] {
		return nil, fs.ErrPermission
	}
	c, ok := f.files[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(c)), nil
}

func (f *fake) Glob(pattern string) ([]string, error) {
	if f.denied[pattern] {
		return nil, fs.ErrPermission
	}
	return []string{}, nil
}

func (f *fake) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	f.executed = append(f.executed, argv)
	key := ""
	for i, a := range argv {
		if i > 0 {
			key += " "
		}
		key += a
	}
	if f.failures[key] {
		return nil, []byte("sudo: a password is required"), 1, nil
	}
	return []byte(f.outputs[key]), nil, 0, nil
}

func (f *fake) Close() error     { return nil }
func (f *fake) Describe() string { return "fake" }

// TestWithoutSudoNothingIsEscalated is the half of the rule DR5 demands: with no flag,
// the transport has to come back exactly as it was, and no command may run. A
// test that only checked the path WITH sudo would let a silent escalation slip
// by unnoticed.
func TestWithoutSudoNothingIsEscalated(t *testing.T) {
	f := newFake()
	f.denied["/etc/nginx/nginx.conf"] = true

	tr := transport.WithPrivilegedRead(context.Background(), f, false)
	_, err := tr.Open("/etc/nginx/nginx.conf")

	require.ErrorIs(t, err, fs.ErrPermission)
	assert.Empty(t, f.executed, "with no --sudo no command may run")
	assert.Same(t, transport.Transport(f), tr, "with no flag the transport comes back untouched")
}

// The escalation is MINIMAL: only the refused file is re-read with privilege.
// In a 132-file configuration where one is restricted, the other 131 must not
// go through sudo at all.
func TestWithSudoOnlyTheRefusedIsElevated(t *testing.T) {
	f := newFake()
	f.files["/etc/nginx/aberto.conf"] = "worker_processes 1;\n"
	f.denied["/etc/nginx/restrito.conf"] = true
	f.outputs["sudo -n cat -- /etc/nginx/restrito.conf"] = "server { listen 80; }\n"

	tr := transport.WithPrivilegedRead(context.Background(), f, true)

	rc, err := tr.Open("/etc/nginx/aberto.conf")
	require.NoError(t, err)
	_ = rc.Close()
	assert.Empty(t, f.executed, "a readable file must not trigger sudo")

	rc, err = tr.Open("/etc/nginx/restrito.conf")
	require.NoError(t, err)
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, "server { listen 80; }\n", string(b))
	require.Len(t, f.executed, 1)
	assert.Equal(t,
		[]string{"sudo", "-n", "cat", "--", "/etc/nginx/restrito.conf"}, f.executed[0],
		"explicit argv, no shell: a file name must not turn into an injection")

	diags := transport.Diagnostics(tr)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityInfo, diags[0].Severity)
	assert.Contains(t, diags[0].Message, "/etc/nginx/restrito.conf",
		"reading a server configuration with privilege cannot happen silently")
}

// A hardened server allows specific commands in sudoers -- typically nginx --
// and refuses `cat`. There the dump is the only way through, and without it
// privileged reading would be useless exactly where sudo is well configured.
func TestWhenSudoDeniesCatTheDumpResolves(t *testing.T) {
	f := newFake()
	f.denied["/etc/nginx/nginx.conf"] = true
	f.failures["sudo -n cat -- /etc/nginx/nginx.conf"] = true

	dump := func(context.Context) (map[string][]byte, error) {
		return map[string][]byte{"/etc/nginx/nginx.conf": []byte("worker_processes 4;\n")}, nil
	}
	tr := transport.WithPrivilegedReadAndDump(context.Background(), f, true, dump)

	rc, err := tr.Open("/etc/nginx/nginx.conf")
	require.NoError(t, err)
	b, _ := io.ReadAll(rc)
	_ = rc.Close()

	assert.Equal(t, "worker_processes 4;\n", string(b))
	diags := transport.Diagnostics(tr)
	require.NotEmpty(t, diags)
	assert.Equal(t, transport.CodeReadViaDump, diags[0].Code,
		"the origin of the content has to show: it came from nginx -T, not from the file")
}

// With no dump and no `cat`, the outcome is a refusal with the reason — never a
// partial tree presented as complete.
func TestWithNoPathRefusesInsteadOfPresentingPartial(t *testing.T) {
	f := newFake()
	f.denied["/etc/nginx/nginx.conf"] = true
	f.failures["sudo -n cat -- /etc/nginx/nginx.conf"] = true

	tr := transport.WithPrivilegedRead(context.Background(), f, true)
	_, err := tr.Open("/etc/nginx/nginx.conf")

	require.ErrorIs(t, err, fs.ErrPermission, "the cause is still permission")
	diags := transport.Diagnostics(tr)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityError, diags[0].Severity)
	assert.Contains(t, diags[0].Message, "sudo")
}

// A path starting with `-` is the ARGUMENT injection case: without the `--`
// closing the options, cat would read "-rf" as a flag instead of as a file.
// Explicit argv solves shell injection and does not solve this one -- they are
// different defects, and the command here runs with privilege.
//
// The path comes from an `include` directive in the target's configuration,
// which is not trusted input.
func TestPathStartingWithDashDoesNotBecomeFlag(t *testing.T) {
	f := newFake()
	suspicious := "/etc/nginx/-rf"
	f.denied[suspicious] = true
	f.outputs["sudo -n cat -- "+suspicious] = "worker_processes 1;\n"

	tr := transport.WithPrivilegedRead(context.Background(), f, true)
	rc, err := tr.Open(suspicious)
	require.NoError(t, err)
	_ = rc.Close()

	require.Len(t, f.executed, 1)
	argv := f.executed[0]
	require.Contains(t, argv, "--", "the end-of-options separator has to be there")
	assert.Less(t, indexOf(argv, "--"), indexOf(argv, suspicious),
		"the separator has to come BEFORE the path to be worth anything")
}

func indexOf(list []string, target string) int {
	for i, v := range list {
		if v == target {
			return i
		}
	}
	return -1
}

// The trust tree is DERIVED from the configuration, never a fixed list of
// paths: a fixed list would break a non-standard installation, and a real
// server we measured includes from /etc/letsencrypt, outside /etc/nginx.
//
// The pair of cases is the point: elevating for a sibling of a file already
// reached is routine and comes out as info; elevating inside a directory the
// configuration had never touched is news, and news involving sudo comes out
// as a warning.
func TestElevationOutsideKnownTreeBecomesWarning(t *testing.T) {
	severityOf := func(diags []output.Diagnostic, code string) output.Severity {
		for _, d := range diags {
			if d.Code == code {
				return d.Severity
			}
		}
		return ""
	}

	t.Run("sibling of an already read file comes out as info", func(t *testing.T) {
		f := newFake()
		f.files["/etc/nginx/nginx.conf"] = "include conf.d/*.conf;\n"
		f.denied["/etc/nginx/conf.d/restrito.conf"] = true
		f.outputs["sudo -n cat -- /etc/nginx/conf.d/restrito.conf"] = "server {}\n"

		tr := transport.WithPrivilegedRead(context.Background(), f, true)
		rc, err := tr.Open("/etc/nginx/nginx.conf")
		require.NoError(t, err)
		_ = rc.Close()
		rc, err = tr.Open("/etc/nginx/conf.d/restrito.conf")
		require.NoError(t, err)
		_ = rc.Close()

		diags := transport.Diagnostics(tr)
		assert.Equal(t, output.SeverityInfo,
			severityOf(diags, transport.CodePrivilegedRead))
		assert.Empty(t, severityOf(diags, transport.CodeElevationOutsideTree),
			"conf.d sits under /etc/nginx, which the configuration already reached")
	})

	t.Run("directory never touched comes out as a warning", func(t *testing.T) {
		f := newFake()
		f.files["/etc/nginx/nginx.conf"] = "include /opt/segredos/x.conf;\n"
		f.denied["/opt/segredos/x.conf"] = true
		f.outputs["sudo -n cat -- /opt/segredos/x.conf"] = "server {}\n"

		tr := transport.WithPrivilegedRead(context.Background(), f, true)
		rc, err := tr.Open("/etc/nginx/nginx.conf")
		require.NoError(t, err)
		_ = rc.Close()
		rc, err = tr.Open("/opt/segredos/x.conf")
		require.NoError(t, err)
		_ = rc.Close()

		diags := transport.Diagnostics(tr)
		assert.Equal(t, output.SeverityWarning,
			severityOf(diags, transport.CodeElevationOutsideTree),
			"elevating in a new directory is the anomaly the warning exists to show")
	})

	t.Run("the top-level file itself is never an anomaly", func(t *testing.T) {
		f := newFake()
		f.denied["/opt/nginx-custom/nginx.conf"] = true
		f.outputs["sudo -n cat -- /opt/nginx-custom/nginx.conf"] = "events {}\n"

		tr := transport.WithPrivilegedRead(context.Background(), f, true)
		rc, err := tr.Open("/opt/nginx-custom/nginx.conf")
		require.NoError(t, err)
		_ = rc.Close()

		diags := transport.Diagnostics(tr)
		assert.Empty(t, severityOf(diags, transport.CodeElevationOutsideTree),
			"the configuration the operator named is not news, wherever it lives")
	})
}
