//go:build windows

package apply

import "os"

// preserveOwner does nothing on Windows.
//
// Ownership there is an ACL rather than a uid/gid pair, os.Chown is a no-op
// that returns an error, and a rename preserves the destination's inherited
// permissions. Pretending otherwise would mean writing code that cannot be
// tested on the platform it claims to serve.
func preserveOwner(tmpName string, want os.FileInfo) error { return nil }

// ownerOf reports no owner on Windows, where ownership is an ACL rather than a
// uid/gid pair.
func ownerOf(info os.FileInfo) (uid, gid int) { return -1, -1 }
