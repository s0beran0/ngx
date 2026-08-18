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
	caminho := binarioDeTeste(t, "versao antiga", 0o755)

	require.NoError(t, Apply(caminho, []byte("versao nova")))

	assert.Equal(t, "versao nova", conteudo(t, caminho))
}

func TestApplyPreservaAPermissaoDoOriginal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modo de arquivo do Windows nao mapeia em permissao unix")
	}
	caminho := binarioDeTeste(t, "antigo", 0o700)

	require.NoError(t, Apply(caminho, []byte("novo")))

	info, err := os.Stat(caminho)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestApplyNaoDeixaTemporarioParaTras(t *testing.T) {
	caminho := binarioDeTeste(t, "antigo", 0o755)

	require.NoError(t, Apply(caminho, []byte("novo")))

	entradas, err := os.ReadDir(filepath.Dir(caminho))
	require.NoError(t, err)
	assert.Len(t, entradas, 1, "sobrou arquivo no diretorio do binario")
}

func TestApplyNaoQuebraOProcessoQueJaAbriuOBinario(t *testing.T) {
	// Em Unix, rename por cima de um executavel em uso funciona porque o
	// inode antigo sobrevive enquanto houver descritor aberto — e escrever
	// por cima e que falharia com ETXTBSY. O descritor aberto aqui faz o
	// papel do processo em execucao.
	if runtime.GOOS == "windows" {
		t.Skip("no Windows o executavel em uso e travado; ver trocaComRenomeio")
	}
	caminho := binarioDeTeste(t, "processo em execucao", 0o755)
	f, err := os.Open(caminho)
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, Apply(caminho, []byte("binario novo")))

	antigo := make([]byte, len("processo em execucao"))
	_, err = f.ReadAt(antigo, 0)
	require.NoError(t, err)
	assert.Equal(t, "processo em execucao", string(antigo),
		"o inode antigo precisa sobreviver para o processo em execucao")
	assert.Equal(t, "binario novo", conteudo(t, caminho))
}

func TestApplyRecusaBinarioVazio(t *testing.T) {
	caminho := binarioDeTeste(t, "antigo", 0o755)

	err := Apply(caminho, nil)

	assert.Equal(t, CodigoArtefatoInvalido, codigoDe(t, err))
	assert.Equal(t, "antigo", conteudo(t, caminho))
}

func TestApplySemPermissaoNoDiretorioExplicaOQueFalta(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissao de diretorio no Windows nao segue o modo unix")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignora a permissao do diretorio")
	}
	dir := t.TempDir()
	caminho := filepath.Join(dir, "ngx")
	require.NoError(t, os.WriteFile(caminho, []byte("antigo"), 0o755))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := Apply(caminho, []byte("novo"))

	assert.Equal(t, CodigoPermissao, codigoDe(t, err))
	assert.Contains(t, err.Error(), dir)
	assert.Contains(t, err.Error(), "privilegio")
	assert.Equal(t, "antigo", conteudo(t, caminho), "o binario atual tem de continuar intacto")
}

// --- sequencia do Windows (DD5), exercitada em qualquer sistema ---

// opsRegistradas embrulha operacoes reais registrando a ordem das chamadas, e
// deixa injetar falha em qualquer passo. E o que torna a sequencia do Windows
// testavel em Linux e macOS.
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
			return errors.New("falha injetada em " + passo)
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
	caminho := binarioDeTeste(t, "ngx antigo", 0o755)
	reg := &opsRegistradas{}

	require.NoError(t, trocaComRenomeio(reg.ops(), caminho, []byte("ngx novo"), 0o755))

	assert.Equal(t, "ngx novo", conteudo(t, caminho))
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
	// No Windows a remocao do .old falha porque o arquivo ainda esta em uso
	// pelo processo que esta rodando. Isso e esperado e nao pode abortar a
	// atualizacao: a limpeza fica para LimparResiduo, na proxima execucao.
	caminho := binarioDeTeste(t, "ngx antigo", 0o755)
	reg := &opsRegistradas{falharEm: "remover ngx.old"}

	require.NoError(t, trocaComRenomeio(reg.ops(), caminho, []byte("ngx novo"), 0o755))

	assert.Equal(t, "ngx novo", conteudo(t, caminho))
	assert.FileExists(t, caminho+sufixoAntigo)
	assert.Equal(t, "ngx antigo", conteudo(t, caminho+sufixoAntigo))
}

func TestTrocaComRenomeioRestauraQuandoOTerceiroPassoFalha(t *testing.T) {
	// O pior desfecho possivel e o usuario ficar sem binario. Se o novo nao
	// entrar no lugar depois de o atual ter saido, o atual volta.
	caminho := binarioDeTeste(t, "ngx antigo", 0o755)
	reg := &opsRegistradas{falharEm: "renomear ngx.new->ngx"}

	err := trocaComRenomeio(reg.ops(), caminho, []byte("ngx novo"), 0o755)

	assert.Equal(t, CodigoTrocaFalhou, codigoDe(t, err))
	assert.Contains(t, err.Error(), "restaurado")
	require.FileExists(t, caminho)
	assert.Equal(t, "ngx antigo", conteudo(t, caminho))
	assert.NoFileExists(t, caminho+".new")
	assert.NoFileExists(t, caminho+sufixoAntigo)
}

func TestTrocaComRenomeioNaoTocaNoBinarioQuandoOSegundoPassoFalha(t *testing.T) {
	caminho := binarioDeTeste(t, "ngx antigo", 0o755)
	reg := &opsRegistradas{falharEm: "renomear ngx->ngx.old"}

	err := trocaComRenomeio(reg.ops(), caminho, []byte("ngx novo"), 0o755)

	assert.Equal(t, CodigoTrocaFalhou, codigoDe(t, err))
	assert.Equal(t, "ngx antigo", conteudo(t, caminho))
	assert.NoFileExists(t, caminho+".new")
}

func TestTrocaComRenomeioDizOndeEstaOBinarioQuandoNemARestauracaoFunciona(t *testing.T) {
	caminho := binarioDeTeste(t, "ngx antigo", 0o755)
	reg := &opsRegistradas{}
	base := reg.ops()
	// Falha no passo 3 e tambem na restauracao: o unico caminho em que o
	// usuario fica sem ngx no lugar. A mensagem tem de dizer onde esta o
	// binario e o que fazer.
	ops := opsArquivo{
		escrever: base.escrever,
		remover:  base.remover,
		renomear: func(de, para string) error {
			if filepath.Base(de) == "ngx.new" || filepath.Base(de) == "ngx.old" {
				return errors.New("falha injetada")
			}
			return base.renomear(de, para)
		},
	}

	err := trocaComRenomeio(ops, caminho, []byte("ngx novo"), 0o755)

	assert.Equal(t, CodigoTrocaFalhou, codigoDe(t, err))
	assert.Contains(t, err.Error(), caminho+sufixoAntigo)
	assert.Contains(t, err.Error(), "a mao")
	// O conteudo antigo continua existindo, ainda que sob outro nome.
	assert.Equal(t, "ngx antigo", conteudo(t, caminho+sufixoAntigo))
}

func TestTrocaComRenomeioLimpaNewDeTentativaAnterior(t *testing.T) {
	caminho := binarioDeTeste(t, "ngx antigo", 0o755)
	require.NoError(t, os.WriteFile(caminho+".new", []byte("lixo de antes"), 0o755))
	reg := &opsRegistradas{}

	require.NoError(t, trocaComRenomeio(reg.ops(), caminho, []byte("ngx novo"), 0o755))

	assert.Equal(t, "ngx novo", conteudo(t, caminho))
}

func TestTrocaComRenomeioSemPermissaoExplicaODiretorio(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissao de diretorio no Windows nao segue o modo unix")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignora a permissao do diretorio")
	}
	dir := t.TempDir()
	caminho := filepath.Join(dir, "ngx")
	require.NoError(t, os.WriteFile(caminho, []byte("antigo"), 0o755))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := trocaComRenomeio(opsReais(), caminho, []byte("novo"), 0o755)

	assert.Equal(t, CodigoPermissao, codigoDe(t, err))
	assert.Equal(t, "antigo", conteudo(t, caminho))
}

func TestErroDeEscritaSeparaPermissaoDeOutrasFalhas(t *testing.T) {
	err := erroDeEscrita(fs.ErrPermission, "/opt/ngx/bin/ngx")
	assert.Equal(t, CodigoPermissao, codigoDe(t, err))
	assert.Contains(t, err.Error(), filepath.FromSlash("/opt/ngx/bin"))

	err = erroDeEscrita(errors.New("disco cheio"), "/opt/ngx/bin/ngx")
	assert.Equal(t, CodigoTrocaFalhou, codigoDe(t, err))
}

func TestLimparResiduoRemoveOAntigo(t *testing.T) {
	caminho := binarioDeTeste(t, "ngx", 0o755)
	require.NoError(t, os.WriteFile(caminho+sufixoAntigo, []byte("orfao"), 0o755))

	LimparResiduo(caminho)

	assert.NoFileExists(t, caminho+sufixoAntigo)
	assert.FileExists(t, caminho)
}

func TestLimparResiduoEhSilenciosaQuandoNaoHaNada(t *testing.T) {
	// Nao devolve erro e nao entra em panico: um orfao que nao existe, ou um
	// caminho vazio na inicializacao, nao sao problema do usuario.
	LimparResiduo(filepath.Join(t.TempDir(), "ngx"))
	LimparResiduo("")
}
