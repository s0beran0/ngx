//go:build !windows

package apply

import (
	"fmt"
	"os"
	"syscall"
)

// preserveOwner copies uid and gid from the original file onto the temporary
// one, before the rename.
//
// It is best-effort ON PURPOSE, and the condition is narrow: chown is refused
// to unprivileged users on Linux and macOS, so an ordinary apply on files the
// user owns would fail on every platform if this were strict. What it must NOT
// do is stay silent when the owner really differs and cannot be set -- that is
// the case where the rename would hand nginx a file owned by the wrong user.
func preserveOwner(tmpName string, want os.FileInfo) error {
	stat, ok := want.Sys().(*syscall.Stat_t)
	if !ok {
		// No ownership information available for this filesystem. Nothing to
		// preserve, and inventing a uid would be worse than leaving it.
		return nil
	}

	uid, gid := int(stat.Uid), int(stat.Gid)

	current, err := os.Stat(tmpName)
	if err != nil {
		return err
	}
	if cur, ok := current.Sys().(*syscall.Stat_t); ok {
		if int(cur.Uid) == uid && int(cur.Gid) == gid {
			// Already right, which is the normal case: the temporary file was
			// created by the same user that owns the target.
			return nil
		}
	}

	if err := os.Chown(tmpName, uid, gid); err != nil {
		return fmt.Errorf("the file is owned by %d:%d and this process cannot set that: %w",
			uid, gid, err)
	}
	return nil
}
