package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ordering is the whole design, so it is asserted directly: the test runs
// FIRST and a failure stops the reload.
func TestReloadTestsTheConfigurationFirst(t *testing.T) {
	f := newFake("local").
		respond("nginx -t", response{stderr: outputTestOK}).
		respond("nginx -s reload", response{})

	res, err := New(f).Reload(context.Background())
	require.NoError(t, err)

	require.NotNil(t, res.Tested)
	assert.True(t, res.Tested.OK)
	assert.True(t, res.Reloaded)

	// Both commands ran, and in that order. A reload that came first would be
	// a reload against bytes nobody validated.
	require.Len(t, f.executed, 2)
	assert.Equal(t, []string{"nginx", "-t"}, f.executed[0])
	assert.Equal(t, []string{"nginx", "-s", "reload"}, f.executed[1])
}

// A refused configuration means no reload, and the proof is that the signal was
// never sent -- not that a flag says so.
func TestAnInvalidConfigurationIsNotReloaded(t *testing.T) {
	f := newFake("local").
		respond("nginx -t", response{stderr: outputTestFailed, exit: 1}).
		respond("nginx -s reload", response{})

	res, err := New(f).Reload(context.Background())

	// Not an error: nginx answered the question that was asked, and the answer
	// was no. The caller decides what exit code that deserves, the way every
	// other command in this package works.
	require.NoError(t, err)
	assert.False(t, res.Tested.OK)
	assert.False(t, res.Reloaded)

	require.Len(t, f.executed, 1, "the reload was attempted despite the test failing")
	assert.Equal(t, []string{"nginx", "-t"}, f.executed[0])
}

// The test passing does not make the reload succeed. A master process that is
// not running is the common case, and it has to come back as "not reloaded"
// with nginx's own words rather than as success.
func TestAFailedReloadIsReportedWithWhatNginxSaid(t *testing.T) {
	f := newFake("local").
		respond("nginx -t", response{stderr: outputTestOK}).
		respond("nginx -s reload", response{
			stderr: "nginx: [error] open() \"/run/nginx.pid\" failed (2: No such file or directory)",
			exit:   1,
		})

	res, err := New(f).Reload(context.Background())
	require.NoError(t, err)

	assert.True(t, res.Tested.OK)
	assert.False(t, res.Reloaded)
	assert.Contains(t, res.Raw, "/run/nginx.pid",
		"the reason nginx gave has to reach the caller")
}
