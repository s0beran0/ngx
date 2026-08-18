//go:build !windows

package update

import (
	"os"
	"path/filepath"
)

// aplicar swaps the binary on Linux and macOS.
//
// Writing OVER an executable in use fails with ETXTBSY; renaming OVER it
// works, because the old inode survives as long as the running process keeps
// the descriptor open. That is why the new binary is written to a temporary
// file IN THE SAME DIRECTORY -- rename does not cross filesystems -- and
// comes in through a rename, which is atomic: at no instant is caminho left
// without a complete binary.
func aplicar(caminho string, novo []byte, perm os.FileMode) error {
	dir := filepath.Dir(caminho)

	tmp, err := os.CreateTemp(dir, ".ngx-update-*")
	if err != nil {
		return erroDeEscrita(err, caminho)
	}
	nomeTmp := tmp.Name()
	tmp.Close()

	if err := escreverArquivo(nomeTmp, novo, perm); err != nil {
		_ = os.Remove(nomeTmp)
		return erroDeEscrita(err, caminho)
	}

	if err := os.Rename(nomeTmp, caminho); err != nil {
		_ = os.Remove(nomeTmp)
		return erroCausa(err, CodigoTrocaFalhou,
			"could not put the new binary at %s; the current ngx stays in place "+
				"and keeps working", caminho)
	}
	return nil
}
