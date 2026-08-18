//go:build windows

package update

import "os"

// aplicar swaps the binary on Windows.
//
// Here the running executable is locked by the system: overwriting and
// deleting fail, renaming works. So the Unix strategy (rename over it) does
// not serve, and the sequence is the one from DD5, implemented in
// trocaComRenomeio: write .new, rename the current one to .old, put the new
// one in place and leave the removal of the .old for the next run
// (LimparResiduo).
//
// Go's os.Rename uses MoveFileEx with MOVEFILE_REPLACE_EXISTING, which is
// enough for both renames: there is no need to call the Windows API directly.
func aplicar(caminho string, novo []byte, perm os.FileMode) error {
	return trocaComRenomeio(opsReais(), caminho, novo, perm)
}
