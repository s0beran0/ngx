package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testBinary(t *testing.T, content string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ngx")
	require.NoError(t, os.WriteFile(path, []byte(content), perm))
	return path
}

func contentOf(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestApplyReplacesTheContent(t *testing.T) {
	path := testBinary(t, "old version", 0o755)

	require.NoError(t, Apply(path, []byte("new version")))

	assert.Equal(t, "new version", contentOf(t, path))
}

func TestApplyPreservesOriginalPermission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows file mode does not map onto unix permissions")
	}
	path := testBinary(t, "old", 0o700)

	require.NoError(t, Apply(path, []byte("new")))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestApplyLeavesNoTemporaryBehind(t *testing.T) {
	path := testBinary(t, "old", 0o755)

	require.NoError(t, Apply(path, []byte("new")))

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "a file was left behind in the binary directory")
}

func TestApplyDoesNotBreakProcessThatAlreadyOpenedTheBinary(t *testing.T) {
	// On Unix, renaming over an executable in use works because the old
	// inode survives while a descriptor stays open -- it is writing over it
	// that would fail with ETXTBSY. The open descriptor here plays the part
	// of the running process.
	if runtime.GOOS == "windows" {
		t.Skip("on Windows the executable in use is locked; see swapByRename")
	}
	path := testBinary(t, "running process", 0o755)
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, Apply(path, []byte("new binary")))

	old := make([]byte, len("running process"))
	_, err = f.ReadAt(old, 0)
	require.NoError(t, err)
	assert.Equal(t, "running process", string(old),
		"the old inode has to survive for the running process")
	assert.Equal(t, "new binary", contentOf(t, path))
}

func TestApplyRejectsEmptyBinary(t *testing.T) {
	path := testBinary(t, "old", 0o755)

	err := Apply(path, nil)

	assert.Equal(t, CodeInvalidArtifact, codeOf(t, err))
	assert.Equal(t, "old", contentOf(t, path))
}

func TestApplyWithoutDirectoryPermissionExplainsWhatIsMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission on Windows does not follow the unix mode")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permission")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ngx")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o755))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := Apply(path, []byte("new"))

	assert.Equal(t, CodePermission, codeOf(t, err))
	assert.Contains(t, err.Error(), dir)
	assert.Contains(t, err.Error(), "privilege")
	assert.Equal(t, "old", contentOf(t, path), "the current binary has to stay intact")
}

// --- the Windows sequence (DD5), exercised on any system ---

// recordedOps wraps the real operations while recording the order of the
// calls, and lets a failure be injected at any step. It is what makes the
// Windows sequence testable on Linux and macOS.
type recordedOps struct {
	calls     []string
	failAt    string
	restoreOK bool
	err       error
}

func (o *recordedOps) ops() fileOps {
	failed := func(step string) error {
		if o.failAt == step {
			if o.err != nil {
				return o.err
			}
			return errors.New("injected failure at " + step)
		}
		return nil
	}
	return fileOps{
		write: func(path string, data []byte, perm os.FileMode) error {
			o.calls = append(o.calls, "write "+filepath.Base(path))
			if err := failed("write"); err != nil {
				return err
			}
			return writeFile(path, data, perm)
		},
		rename: func(from, to string) error {
			step := "rename " + filepath.Base(from) + "->" + filepath.Base(to)
			o.calls = append(o.calls, step)
			if err := failed(step); err != nil {
				return err
			}
			return os.Rename(from, to)
		},
		remove: func(path string) error {
			o.calls = append(o.calls, "remove "+filepath.Base(path))
			if err := failed("remove " + filepath.Base(path)); err != nil {
				return err
			}
			return os.Remove(path)
		},
	}
}

func TestSwapByRenameFollowsTheDD5Sequence(t *testing.T) {
	path := testBinary(t, "old ngx", 0o755)
	reg := &recordedOps{}

	require.NoError(t, swapByRename(reg.ops(), path, []byte("new ngx"), 0o755))

	assert.Equal(t, "new ngx", contentOf(t, path))
	assert.Equal(t, []string{
		"remove ngx.new",
		"write ngx.new",
		"rename ngx->ngx.old",
		"rename ngx.new->ngx",
		"remove ngx.old",
	}, reg.calls)
	assert.NoFileExists(t, path+".new")
}

func TestSwapByRenameIgnoresFailureRemovingTheOld(t *testing.T) {
	// On Windows the removal of the .old fails because the file is still in
	// use by the running process. That is expected and must not abort the
	// update: the cleanup is left to CleanLeftovers, on the next run.
	path := testBinary(t, "old ngx", 0o755)
	reg := &recordedOps{failAt: "remove ngx.old"}

	require.NoError(t, swapByRename(reg.ops(), path, []byte("new ngx"), 0o755))

	assert.Equal(t, "new ngx", contentOf(t, path))
	assert.FileExists(t, path+oldSuffix)
	assert.Equal(t, "old ngx", contentOf(t, path+oldSuffix))
}

func TestSwapByRenameRestoresWhenTheThirdStepFails(t *testing.T) {
	// The worst possible outcome is the user being left without a binary. If
	// the new one does not get into place after the current one has left,
	// the current one comes back.
	path := testBinary(t, "old ngx", 0o755)
	reg := &recordedOps{failAt: "rename ngx.new->ngx"}

	err := swapByRename(reg.ops(), path, []byte("new ngx"), 0o755)

	assert.Equal(t, CodeSwapFailed, codeOf(t, err))
	assert.Contains(t, err.Error(), "restored")
	require.FileExists(t, path)
	assert.Equal(t, "old ngx", contentOf(t, path))
	assert.NoFileExists(t, path+".new")
	assert.NoFileExists(t, path+oldSuffix)
}

func TestSwapByRenameDoesNotTouchTheBinaryWhenTheSecondStepFails(t *testing.T) {
	path := testBinary(t, "old ngx", 0o755)
	reg := &recordedOps{failAt: "rename ngx->ngx.old"}

	err := swapByRename(reg.ops(), path, []byte("new ngx"), 0o755)

	assert.Equal(t, CodeSwapFailed, codeOf(t, err))
	assert.Equal(t, "old ngx", contentOf(t, path))
	assert.NoFileExists(t, path+".new")
}

func TestSwapByRenameSaysWhereTheBinaryIsWhenEvenTheRestoreFails(t *testing.T) {
	path := testBinary(t, "old ngx", 0o755)
	reg := &recordedOps{}
	base := reg.ops()
	// A failure at step 3 and also at the restore: the only path in which the
	// user is left with no ngx in place. The message has to say where the
	// binary is and what to do.
	ops := fileOps{
		write:  base.write,
		remove: base.remove,
		rename: func(from, to string) error {
			if filepath.Base(from) == "ngx.new" || filepath.Base(from) == "ngx.old" {
				return errors.New("injected failure")
			}
			return base.rename(from, to)
		},
	}

	err := swapByRename(ops, path, []byte("new ngx"), 0o755)

	assert.Equal(t, CodeSwapFailed, codeOf(t, err))
	assert.Contains(t, err.Error(), path+oldSuffix)
	assert.Contains(t, err.Error(), "by hand")
	// The old content still exists, even if under another name.
	assert.Equal(t, "old ngx", contentOf(t, path+oldSuffix))
}

func TestSwapByRenameCleansNewFromPreviousAttempt(t *testing.T) {
	path := testBinary(t, "old ngx", 0o755)
	require.NoError(t, os.WriteFile(path+".new", []byte("junk from before"), 0o755))
	reg := &recordedOps{}

	require.NoError(t, swapByRename(reg.ops(), path, []byte("new ngx"), 0o755))

	assert.Equal(t, "new ngx", contentOf(t, path))
}

func TestSwapByRenameWithoutPermissionExplainsTheDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission on Windows does not follow the unix mode")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permission")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ngx")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o755))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := swapByRename(realOps(), path, []byte("new"), 0o755)

	assert.Equal(t, CodePermission, codeOf(t, err))
	assert.Equal(t, "old", contentOf(t, path))
}

func TestWriteErrorSeparatesPermissionFromOtherFailures(t *testing.T) {
	err := writeError(fs.ErrPermission, "/opt/ngx/bin/ngx")
	assert.Equal(t, CodePermission, codeOf(t, err))
	assert.Contains(t, err.Error(), filepath.FromSlash("/opt/ngx/bin"))

	err = writeError(errors.New("disk full"), "/opt/ngx/bin/ngx")
	assert.Equal(t, CodeSwapFailed, codeOf(t, err))
}

func TestCleanLeftoversRemovesTheOld(t *testing.T) {
	path := testBinary(t, "ngx", 0o755)
	require.NoError(t, os.WriteFile(path+oldSuffix, []byte("orphan"), 0o755))

	CleanLeftovers(path)

	assert.NoFileExists(t, path+oldSuffix)
	assert.FileExists(t, path)
}

func TestCleanLeftoversIsSilentWhenThereIsNothing(t *testing.T) {
	// It returns no error and does not panic: an orphan that does not exist,
	// or an empty path at startup, are not the user's problem.
	CleanLeftovers(filepath.Join(t.TempDir(), "ngx"))
	CleanLeftovers("")
}
