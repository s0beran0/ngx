package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// oldSuffix is the name the binary in use receives on Windows while the
// new one takes its place (DD5). It lives here, and not in the file with the
// build tag, because CleanLeftovers runs on every system.
const oldSuffix = ".old"

// defaultPerm is the permission applied when the current binary does not exist
// (the case of a fresh install into an empty directory). Executable by
// everyone, writable only by the owner.
const defaultPerm os.FileMode = 0o755

// Apply replaces the binary at path with the contents of newBinary.
//
// The swap happens after verification, never before, and it never writes over
// the file in use: on Unix the new binary goes to a temporary file in the
// same directory and comes in through a rename; on Windows the current one is
// renamed to .old and the new one takes its place (DD5). In both cases, a
// failure midway leaves a working ngx at path.
func Apply(path string, newBinary []byte) error {
	if len(newBinary) == 0 {
		return newError(CodeInvalidArtifact,
			"the new binary came out empty; the replacement was aborted and the "+
				"current ngx stays in place")
	}
	perm := defaultPerm
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	return apply(path, newBinary, perm)
}

// CleanLeftovers removes the .old binary left behind by a previous update on
// Windows, where the executable in use cannot be deleted (DD5). It should be
// called at ngx startup and fails silently on purpose: an orphan file is not
// the user's problem, and an error here has nothing to do with the command
// they asked for. Without this cleanup, every update leaves an orphan binary
// in the directory forever.
func CleanLeftovers(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path + oldSuffix)
}

// fileOps abstracts the file operations of the rename-based swap. It
// exists so that the Windows sequence -- the part whose failure mode is "user
// left without ngx.exe" -- is testable on any system, including with failures
// injected at each step.
type fileOps struct {
	write  func(path string, data []byte, perm os.FileMode) error
	rename func(from, to string) error
	remove func(path string) error
}

func realOps() fileOps {
	return fileOps{
		write:  writeFile,
		rename: os.Rename,
		remove: os.Remove,
	}
}

// swapByRename implements the Windows sequence (DD5), where the
// executable in use can be renamed but neither deleted nor overwritten:
//
//  1. writes the new one as <path>.new;
//  2. renames <path> to <path>.old;
//  3. renames <path>.new to <path>;
//  4. tries to remove the .old and IGNORES the failure -- the file is still
//     in use by the running process, and its removal is left for the next
//     run, via CleanLeftovers.
//
// If step 3 fails after step 2 succeeded, the .old goes back into place.
// Leaving the user without a binary is the worst possible outcome of this
// function -- worse than not updating.
func swapByRename(ops fileOps, path string, newBinary []byte, perm os.FileMode) error {
	tmpNew := path + ".new"
	old := path + oldSuffix

	// A .new left over from a previous attempt would get in the way of the
	// rename.
	_ = ops.remove(tmpNew)

	if err := ops.write(tmpNew, newBinary, perm); err != nil {
		return writeError(err, path)
	}

	if err := ops.rename(path, old); err != nil {
		_ = ops.remove(tmpNew)
		return wrapError(err, CodeSwapFailed,
			"could not move the current binary %s to %s; nothing was swapped and "+
				"the current ngx keeps working", path, old)
	}

	if err := ops.rename(tmpNew, path); err != nil {
		// Restore before anything else: at this point path is empty,
		// and this is the only state in which the user would be left
		// without ngx.
		if errRestore := ops.rename(old, path); errRestore != nil {
			_ = ops.remove(tmpNew)
			return wrapError(err, CodeSwapFailed,
				"the binary swap failed midway and the restore failed too: the previous "+
					"binary is at %s and needs to be renamed back to %s by hand",
				old, path)
		}
		_ = ops.remove(tmpNew)
		return wrapError(err, CodeSwapFailed,
			"could not put the new binary at %s; the previous one was restored "+
				"and keeps working", path)
	}

	// An expected failure while the current process keeps running from the
	// renamed file. CleanLeftovers takes care of it on the next run.
	_ = ops.remove(old)
	return nil
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	// fsync before the rename: without it, a power cut between the two can
	// leave, in place of the binary, a file of the right size and null
	// content.
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	// The requested mode may have been reduced by the umask at creation.
	return os.Chmod(path, perm)
}

// writeError tells a missing permission apart from any other IO failure.
// ngx does not try to escalate privilege on its own: it says which directory
// needs to be writable and stops.
func writeError(cause error, path string) error {
	dir := filepath.Dir(path)
	if errors.Is(cause, fs.ErrPermission) {
		return wrapError(cause, CodePermission,
			"no permission to write to %s, which is where the ngx binary lives. "+
				"The update needs write privilege on that directory: "+
				"retry the command with privilege or install ngx into a directory "+
				"of your own user. Nothing was swapped", dir)
	}
	return wrapError(cause, CodeSwapFailed,
		"could not write the new binary to %s; nothing was swapped and the current "+
			"ngx keeps working", dir)
}
