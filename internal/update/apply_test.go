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

func binarioDeTeste(t *testing.T, conteudo string, perm os.FileMode) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "ngx")
	require.NoError(t, os.WriteFile(caminho, []byte(conteudo), perm))
	return caminho
}

func conteudo(t *testing.T, caminho string) string {
	t.Helper()
	b, err := os.ReadFile(caminho)
	require.NoError(t, err)
	return string(b)
}

func TestApplyTrocaOConteudo(t *testing.T) {
	caminho := binarioDeTeste(t, "old version", 0o755)

	require.NoError(t, Apply(caminho, []byte("new version")))

	assert.Equal(t, "new version", conteudo(t, caminho))
}

func TestApplyPreservaAPermissaoDoOriginal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows file mode does not map onto unix permissions")
	}
	caminho := binarioDeTeste(t, "old", 0o700)

	require.NoError(t, Apply(caminho, []byte("new")))

	info, err := os.Stat(caminho)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestApplyNaoDeixaTemporarioParaTras(t *testing.T) {
	caminho := binarioDeTeste(t, "old", 0o755)

	require.NoError(t, Apply(caminho, []byte("new")))

	entradas, err := os.ReadDir(filepath.Dir(caminho))
	require.NoError(t, err)
	assert.Len(t, entradas, 1, "a file was left behind in the binary directory")
}

func TestApplyNaoQuebraOProcessoQueJaAbriuOBinario(t *testing.T) {
	// On Unix, renaming over an executable in use works because the old
	// inode survives while a descriptor stays open -- it is writing over it
	// that would fail with ETXTBSY. The open descriptor here plays the part
	// of the running process.
	if runtime.GOOS == "windows" {
		t.Skip("on Windows the executable in use is locked; see trocaComRenomeio")
	}
	caminho := binarioDeTeste(t, "running process", 0o755)
	f, err := os.Open(caminho)
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, Apply(caminho, []byte("new binary")))

	antigo := make([]byte, len("running process"))
	_, err = f.ReadAt(antigo, 0)
	require.NoError(t, err)
	assert.Equal(t, "running process", string(antigo),
		"the old inode has to survive for the running process")
	assert.Equal(t, "new binary", conteudo(t, caminho))
}

func TestApplyRecusaBinarioVazio(t *testing.T) {
	caminho := binarioDeTeste(t, "old", 0o755)

	err := Apply(caminho, nil)

	assert.Equal(t, CodigoArtefatoInvalido, codigoDe(t, err))
	assert.Equal(t, "old", conteudo(t, caminho))
}

func TestApplySemPermissaoNoDiretorioExplicaOQueFalta(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission on Windows does not follow the unix mode")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permission")
	}
	dir := t.TempDir()
	caminho := filepath.Join(dir, "ngx")
	require.NoError(t, os.WriteFile(caminho, []byte("old"), 0o755))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := Apply(caminho, []byte("new"))

	assert.Equal(t, CodigoPermissao, codigoDe(t, err))
	assert.Contains(t, err.Error(), dir)
	assert.Contains(t, err.Error(), "privilege")
	assert.Equal(t, "old", conteudo(t, caminho), "the current binary has to stay intact")
}

// --- the Windows sequence (DD5), exercised on any system ---

// opsRegistradas wraps the real operations while recording the order of the
// calls, and lets a failure be injected at any step. It is what makes the
// Windows sequence testable on Linux and macOS.
type opsRegistradas struct {
	chamadas   []string
	falharEm   string
	restaurarK bool
	erro       error
}

func (o *opsRegistradas) ops() opsArquivo {
	falhou := func(passo string) error {
		if o.falharEm == passo {
			if o.erro != nil {
				return o.erro
			}
			return errors.New("injected failure at " + passo)
		}
		return nil
	}
	return opsArquivo{
		escrever: func(caminho string, dados []byte, perm os.FileMode) error {
			o.chamadas = append(o.chamadas, "escrever "+filepath.Base(caminho))
			if err := falhou("escrever"); err != nil {
				return err
			}
			return escreverArquivo(caminho, dados, perm)
		},
		renomear: func(de, para string) error {
			passo := "renomear " + filepath.Base(de) + "->" + filepath.Base(para)
			o.chamadas = append(o.chamadas, passo)
			if err := falhou(passo); err != nil {
				return err
			}
			return os.Rename(de, para)
		},
		remover: func(caminho string) error {
			o.chamadas = append(o.chamadas, "remover "+filepath.Base(caminho))
			if err := falhou("remover " + filepath.Base(caminho)); err != nil {
				return err
			}
			return os.Remove(caminho)
		},
	}
}

func TestTrocaComRenomeioSegueASequenciaDeDD5(t *testing.T) {
	caminho := binarioDeTeste(t, "old ngx", 0o755)
	reg := &opsRegistradas{}

	require.NoError(t, trocaComRenomeio(reg.ops(), caminho, []byte("new ngx"), 0o755))

	assert.Equal(t, "new ngx", conteudo(t, caminho))
	assert.Equal(t, []string{
		"remover ngx.new",
		"escrever ngx.new",
		"renomear ngx->ngx.old",
		"renomear ngx.new->ngx",
		"remover ngx.old",
	}, reg.chamadas)
	assert.NoFileExists(t, caminho+".new")
}

func TestTrocaComRenomeioIgnoraFalhaAoRemoverOAntigo(t *testing.T) {
	// On Windows the removal of the .old fails because the file is still in
	// use by the running process. That is expected and must not abort the
	// update: the cleanup is left to LimparResiduo, on the next run.
	caminho := binarioDeTeste(t, "old ngx", 0o755)
	reg := &opsRegistradas{falharEm: "remover ngx.old"}

	require.NoError(t, trocaComRenomeio(reg.ops(), caminho, []byte("new ngx"), 0o755))

	assert.Equal(t, "new ngx", conteudo(t, caminho))
	assert.FileExists(t, caminho+sufixoAntigo)
	assert.Equal(t, "old ngx", conteudo(t, caminho+sufixoAntigo))
}

func TestTrocaComRenomeioRestauraQuandoOTerceiroPassoFalha(t *testing.T) {
	// The worst possible outcome is the user being left without a binary. If
	// the new one does not get into place after the current one has left,
	// the current one comes back.
	caminho := binarioDeTeste(t, "old ngx", 0o755)
	reg := &opsRegistradas{falharEm: "renomear ngx.new->ngx"}

	err := trocaComRenomeio(reg.ops(), caminho, []byte("new ngx"), 0o755)

	assert.Equal(t, CodigoTrocaFalhou, codigoDe(t, err))
	assert.Contains(t, err.Error(), "restored")
	require.FileExists(t, caminho)
	assert.Equal(t, "old ngx", conteudo(t, caminho))
	assert.NoFileExists(t, caminho+".new")
	assert.NoFileExists(t, caminho+sufixoAntigo)
}

func TestTrocaComRenomeioNaoTocaNoBinarioQuandoOSegundoPassoFalha(t *testing.T) {
	caminho := binarioDeTeste(t, "old ngx", 0o755)
	reg := &opsRegistradas{falharEm: "renomear ngx->ngx.old"}

	err := trocaComRenomeio(reg.ops(), caminho, []byte("new ngx"), 0o755)

	assert.Equal(t, CodigoTrocaFalhou, codigoDe(t, err))
	assert.Equal(t, "old ngx", conteudo(t, caminho))
	assert.NoFileExists(t, caminho+".new")
}

func TestTrocaComRenomeioDizOndeEstaOBinarioQuandoNemARestauracaoFunciona(t *testing.T) {
	caminho := binarioDeTeste(t, "old ngx", 0o755)
	reg := &opsRegistradas{}
	base := reg.ops()
	// A failure at step 3 and also at the restore: the only path in which the
	// user is left with no ngx in place. The message has to say where the
	// binary is and what to do.
	ops := opsArquivo{
		escrever: base.escrever,
		remover:  base.remover,
		renomear: func(de, para string) error {
			if filepath.Base(de) == "ngx.new" || filepath.Base(de) == "ngx.old" {
				return errors.New("injected failure")
			}
			return base.renomear(de, para)
		},
	}

	err := trocaComRenomeio(ops, caminho, []byte("new ngx"), 0o755)

	assert.Equal(t, CodigoTrocaFalhou, codigoDe(t, err))
	assert.Contains(t, err.Error(), caminho+sufixoAntigo)
	assert.Contains(t, err.Error(), "by hand")
	// The old content still exists, even if under another name.
	assert.Equal(t, "old ngx", conteudo(t, caminho+sufixoAntigo))
}

func TestTrocaComRenomeioLimpaNewDeTentativaAnterior(t *testing.T) {
	caminho := binarioDeTeste(t, "old ngx", 0o755)
	require.NoError(t, os.WriteFile(caminho+".new", []byte("junk from before"), 0o755))
	reg := &opsRegistradas{}

	require.NoError(t, trocaComRenomeio(reg.ops(), caminho, []byte("new ngx"), 0o755))

	assert.Equal(t, "new ngx", conteudo(t, caminho))
}

func TestTrocaComRenomeioSemPermissaoExplicaODiretorio(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission on Windows does not follow the unix mode")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permission")
	}
	dir := t.TempDir()
	caminho := filepath.Join(dir, "ngx")
	require.NoError(t, os.WriteFile(caminho, []byte("old"), 0o755))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := trocaComRenomeio(opsReais(), caminho, []byte("new"), 0o755)

	assert.Equal(t, CodigoPermissao, codigoDe(t, err))
	assert.Equal(t, "old", conteudo(t, caminho))
}

func TestErroDeEscritaSeparaPermissaoDeOutrasFalhas(t *testing.T) {
	err := erroDeEscrita(fs.ErrPermission, "/opt/ngx/bin/ngx")
	assert.Equal(t, CodigoPermissao, codigoDe(t, err))
	assert.Contains(t, err.Error(), filepath.FromSlash("/opt/ngx/bin"))

	err = erroDeEscrita(errors.New("disk full"), "/opt/ngx/bin/ngx")
	assert.Equal(t, CodigoTrocaFalhou, codigoDe(t, err))
}

func TestLimparResiduoRemoveOAntigo(t *testing.T) {
	caminho := binarioDeTeste(t, "ngx", 0o755)
	require.NoError(t, os.WriteFile(caminho+sufixoAntigo, []byte("orphan"), 0o755))

	LimparResiduo(caminho)

	assert.NoFileExists(t, caminho+sufixoAntigo)
	assert.FileExists(t, caminho)
}

func TestLimparResiduoEhSilenciosaQuandoNaoHaNada(t *testing.T) {
	// It returns no error and does not panic: an orphan that does not exist,
	// or an empty path at startup, are not the user's problem.
	LimparResiduo(filepath.Join(t.TempDir(), "ngx"))
	LimparResiduo("")
}
