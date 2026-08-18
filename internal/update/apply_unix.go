//go:build !windows

package update

import (
	"os"
	"path/filepath"
)

// apply swaps the binary on Linux and macOS.
//
// Writing OVER an executable in use fails with ETXTBSY; renaming OVER it
// works, because the old inode survives as long as the running process keeps
// the descriptor open. That is why the new binary is written to a temporary
// file IN THE SAME DIRECTORY -- rename does not cross filesystems -- and
// comes in through a rename, which is atomic: at no instant is path left
// without a complete binary.
func apply(path string, newBinary []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, ".ngx-update-*")
	if err != nil {
		return writeError(err, path)
	}
	tmpName := tmp.Name()
	tmp.Close()

	if err := writeFile(tmpName, newBinary, perm); err != nil {
		_ = os.Remove(tmpName)
		return writeError(err, path)
	}

	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return wrapError(err, CodigoTrocaFalhou,
			"could not put the new binary at %s; the current ngx stays in place "+
				"and keeps working", path)
	}
	return nil
}
