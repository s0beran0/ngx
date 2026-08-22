package apply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// An internal test, because the failure it needs is in writeAtomically's rename
// and there is no way to provoke that through Run: every path Run can reach
// either fails before the temporary file exists or succeeds.
//
// It exists because negative verification said it was missing. Removing the
// cleanup on the rename-failure path left the whole suite green, which means
// nothing was checking that a failed apply leaves the directory as it found it.
func TestAFailedRenameLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()

	// A directory where the target should be. Stat succeeds, so the function
	// gets as far as creating the temporary file and writing it; the rename
	// then fails, because a file cannot replace a directory.
	target := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.Mkdir(target, 0o755))

	err := writeAtomically(target, []byte("events {}\n"), nil)
	require.Error(t, err, "renaming a file over a directory has to fail")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.Falsef(t, strings.HasPrefix(e.Name(), ".ngx-apply-"),
			"the temporary file survived the failed rename: %s", e.Name())
	}
}

// What is NOT verified here, stated rather than left implicit.
//
// The fsync before the rename cannot be tested in this suite: its absence only
// shows after a power loss or a kernel crash, where a rename that lands before
// the data leaves a file of the right size and null content. Removing the Sync
// call leaves every test in this package green, which was confirmed on purpose.
//
// So it is justified by inheritance rather than by a test: the same sequence,
// with the same reasoning, is in internal/update/apply.go, where it guards the
// ngx binary. If that reasoning is ever revisited, both places change together.
//
// This comment is the deliverable. An untested invariant that nobody wrote down
// is indistinguishable from one nobody thought of.
func TestFsyncIsNotCoveredByATest(t *testing.T) {
	t.Skip("documentation: see the comment above -- fsync cannot be verified without a power loss")
}
