package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// sufixoAntigo is the name the binary in use receives on Windows while the
// new one takes its place (DD5). It lives here, and not in the file with the
// build tag, because LimparResiduo runs on every system.
const sufixoAntigo = ".old"

// permPadrao is the permission applied when the current binary does not exist
// (the case of a fresh install into an empty directory). Executable by
// everyone, writable only by the owner.
const permPadrao os.FileMode = 0o755

// Apply replaces the binary at caminho with the contents of novo.
//
// The swap happens after verification, never before, and it never writes over
// the file in use: on Unix the new binary goes to a temporary file in the
// same directory and comes in through a rename; on Windows the current one is
// renamed to .old and the new one takes its place (DD5). In both cases, a
// failure midway leaves a working ngx at caminho.
func Apply(caminho string, novo []byte) error {
	if len(novo) == 0 {
		return erro(CodigoArtefatoInvalido,
			"the new binary came out empty; the replacement was aborted and the "+
				"current ngx stays in place")
	}
	perm := permPadrao
	if info, err := os.Stat(caminho); err == nil {
		perm = info.Mode().Perm()
	}
	return aplicar(caminho, novo, perm)
}

// LimparResiduo removes the .old binary left behind by a previous update on
// Windows, where the executable in use cannot be deleted (DD5). It should be
// called at ngx startup and fails silently on purpose: an orphan file is not
// the user's problem, and an error here has nothing to do with the command
// they asked for. Without this cleanup, every update leaves an orphan binary
// in the directory forever.
func LimparResiduo(caminho string) {
	if caminho == "" {
		return
	}
	_ = os.Remove(caminho + sufixoAntigo)
}

// opsArquivo abstracts the file operations of the rename-based swap. It
// exists so that the Windows sequence -- the part whose failure mode is "user
// left without ngx.exe" -- is testable on any system, including with failures
// injected at each step.
type opsArquivo struct {
	escrever func(caminho string, dados []byte, perm os.FileMode) error
	renomear func(de, para string) error
	remover  func(caminho string) error
}

func opsReais() opsArquivo {
	return opsArquivo{
		escrever: escreverArquivo,
		renomear: os.Rename,
		remover:  os.Remove,
	}
}

// trocaComRenomeio implements the Windows sequence (DD5), where the
// executable in use can be renamed but neither deleted nor overwritten:
//
//  1. writes the new one as <caminho>.new;
//  2. renames <caminho> to <caminho>.old;
//  3. renames <caminho>.new to <caminho>;
//  4. tries to remove the .old and IGNORES the failure -- the file is still
//     in use by the running process, and its removal is left for the next
//     run, via LimparResiduo.
//
// If step 3 fails after step 2 succeeded, the .old goes back into place.
// Leaving the user without a binary is the worst possible outcome of this
// function -- worse than not updating.
func trocaComRenomeio(ops opsArquivo, caminho string, novo []byte, perm os.FileMode) error {
	novoTmp := caminho + ".new"
	antigo := caminho + sufixoAntigo

	// A .new left over from a previous attempt would get in the way of the
	// rename.
	_ = ops.remover(novoTmp)

	if err := ops.escrever(novoTmp, novo, perm); err != nil {
		return erroDeEscrita(err, caminho)
	}

	if err := ops.renomear(caminho, antigo); err != nil {
		_ = ops.remover(novoTmp)
		return erroCausa(err, CodigoTrocaFalhou,
			"could not move the current binary %s to %s; nothing was swapped and "+
				"the current ngx keeps working", caminho, antigo)
	}

	if err := ops.renomear(novoTmp, caminho); err != nil {
		// Restore before anything else: at this point caminho is empty,
		// and this is the only state in which the user would be left
		// without ngx.
		if errRestauro := ops.renomear(antigo, caminho); errRestauro != nil {
			_ = ops.remover(novoTmp)
			return erroCausa(err, CodigoTrocaFalhou,
				"the binary swap failed midway and the restore failed too: the previous "+
					"binary is at %s and needs to be renamed back to %s by hand",
				antigo, caminho)
		}
		_ = ops.remover(novoTmp)
		return erroCausa(err, CodigoTrocaFalhou,
			"could not put the new binary at %s; the previous one was restored "+
				"and keeps working", caminho)
	}

	// An expected failure while the current process keeps running from the
	// renamed file. LimparResiduo takes care of it on the next run.
	_ = ops.remover(antigo)
	return nil
}

func escreverArquivo(caminho string, dados []byte, perm os.FileMode) error {
	f, err := os.OpenFile(caminho, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(dados); err != nil {
		f.Close()
		_ = os.Remove(caminho)
		return err
	}
	// fsync before the rename: without it, a power cut between the two can
	// leave, in place of the binary, a file of the right size and null
	// content.
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(caminho)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(caminho)
		return err
	}
	// The requested mode may have been reduced by the umask at creation.
	return os.Chmod(caminho, perm)
}

// erroDeEscrita tells a missing permission apart from any other IO failure.
// ngx does not try to escalate privilege on its own: it says which directory
// needs to be writable and stops.
func erroDeEscrita(causa error, caminho string) error {
	dir := filepath.Dir(caminho)
	if errors.Is(causa, fs.ErrPermission) {
		return erroCausa(causa, CodigoPermissao,
			"no permission to write to %s, which is where the ngx binary lives. "+
				"The update needs write privilege on that directory: "+
				"retry the command with privilege or install ngx into a directory "+
				"of your own user. Nothing was swapped", dir)
	}
	return erroCausa(causa, CodigoTrocaFalhou,
		"could not write the new binary to %s; nothing was swapped and the current "+
			"ngx keeps working", dir)
}
