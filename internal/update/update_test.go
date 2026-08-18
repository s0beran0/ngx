package update

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"aead.dev/minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cenario monta uma release completa e servida por httptest: artefato,
// checksums.txt e checksums.txt.minisig assinado com um par gerado na hora.
type cenario struct {
	srv       *servidorFalso
	chave     string
	artefato  []byte
	servido   []byte
	nomeAsset string
	binario   []byte
}

func novoCenario(t *testing.T, versao, conteudoBinario string, opcoes ...func(*cenario)) *cenario {
	t.Helper()
	pub, priv := parDeChaves(t)

	c := &cenario{
		srv:       novoServidor(t),
		chave:     textoDaChave(t, pub),
		nomeAsset: "ngx_" + versao + "_linux_amd64.tar.gz",
		binario:   []byte(conteudoBinario),
	}
	c.artefato = tarGzCom(t, map[string][]byte{"ngx": c.binario, "LICENSE": []byte("MIT")})
	for _, o := range opcoes {
		o(c)
	}

	somas := checksumsPara(map[string][]byte{c.nomeAsset: c.artefato})
	sig := minisign.Sign(priv, somas)

	// O checksums.txt cobre c.artefato; o servidor entrega c.servido quando o
	// cenario pede adulteracao. Sao dois campos justamente porque calcular o
	// checksum sobre os bytes adulterados provaria o contrario do pretendido.
	servido := c.artefato
	if c.servido != nil {
		servido = c.servido
	}
	c.srv.respondeBytes("/dl/"+c.nomeAsset, servido)
	c.srv.respondeBytes("/dl/"+NomeChecksums, somas)
	c.srv.respondeBytes("/dl/"+NomeAssinatura, sig)

	rel := Release{Version: "v" + versao, Assets: []Asset{
		{Name: c.nomeAsset, URL: c.srv.URL + "/dl/" + c.nomeAsset},
		{Name: NomeChecksums, URL: c.srv.URL + "/dl/" + NomeChecksums},
		{Name: NomeAssinatura, URL: c.srv.URL + "/dl/" + NomeAssinatura},
	}}
	c.srv.respondeJSON("/repos/s0beran0/ngx/releases/latest", rel)
	c.srv.respondeJSON("/repos/s0beran0/ngx/releases", []Release{rel})
	c.srv.respondeJSON("/repos/s0beran0/ngx/releases/tags/v"+versao, rel)
	return c
}

// corrompeArtefato entrega bytes diferentes daqueles cobertos pelo
// checksums.txt assinado — o caso de download adulterado ou corrompido.
func corrompeArtefato(t *testing.T) func(*cenario) {
	t.Helper()
	return func(c *cenario) {
		c.servido = tarGzCom(t, map[string][]byte{"ngx": []byte("BINARIO ADULTERADO")})
		require.NotEqual(t, c.artefato, c.servido,
			"o artefato servido tem que diferir do que o checksums.txt cobre")
	}
}

func (c *cenario) opcoes(caminho, versaoAtual string) Opcoes {
	return Opcoes{
		Canal:                ChannelStable,
		VersaoAtual:          versaoAtual,
		CaminhoBinario:       caminho,
		ChavePublicaOverride: c.chave,
		Cliente:              c.srv.cliente(),
		SO:                   "linux",
		Arch:                 "amd64",
	}
}

func TestExecutarAtualizaNoCaminhoFeliz(t *testing.T) {
	c := novoCenario(t, "0.3.0", "ngx v0.3.0")
	caminho := binarioDeTeste(t, "ngx v0.2.0", 0o755)

	res, err := Executar(context.Background(), c.opcoes(caminho, "v0.2.0"))

	require.NoError(t, err)
	assert.True(t, res.Atualizado)
	assert.True(t, res.Disponivel)
	assert.Equal(t, "v0.3.0", res.VersaoRemota)
	assert.Equal(t, ChannelStable, res.Canal)
	assert.Equal(t, "ngx v0.3.0", conteudo(t, caminho))
}

func TestExecutarComVerificacaoFalhandoPreservaOBinarioAtual(t *testing.T) {
	// O teste que mais importa deste pacote: um artefato que nao bate com o
	// checksums.txt assinado nao pode chegar perto do binario em uso. Depois
	// da falha, o ngx atual precisa estar intacto byte a byte, com a mesma
	// permissao, e sem arquivo temporario largado no diretorio.
	c := novoCenario(t, "0.3.0", "ngx v0.3.0", corrompeArtefato(t))
	caminho := binarioDeTeste(t, "ngx v0.2.0 EM USO", 0o755)
	antes, err := os.Stat(caminho)
	require.NoError(t, err)

	res, err := Executar(context.Background(), c.opcoes(caminho, "v0.2.0"))

	require.Nil(t, res)
	assert.Equal(t, CodigoChecksumDivergente, codigoDe(t, err))
	assert.Equal(t, "ngx v0.2.0 EM USO", conteudo(t, caminho))

	depois, err := os.Stat(caminho)
	require.NoError(t, err)
	assert.Equal(t, antes.Mode().Perm(), depois.Mode().Perm())
	assert.Equal(t, antes.Size(), depois.Size())

	entradas, err := os.ReadDir(filepath.Dir(caminho))
	require.NoError(t, err)
	assert.Len(t, entradas, 1, "a falha de verificacao deixou lixo no diretorio")
}

func TestExecutarComAssinaturaDeOutraChavePreservaOBinarioAtual(t *testing.T) {
	c := novoCenario(t, "0.3.0", "ngx v0.3.0")
	outraPub, _ := parDeChaves(t)
	caminho := binarioDeTeste(t, "ngx v0.2.0", 0o755)

	opts := c.opcoes(caminho, "v0.2.0")
	opts.ChavePublicaOverride = textoDaChave(t, outraPub)

	_, err := Executar(context.Background(), opts)

	assert.Equal(t, CodigoAssinaturaInvalida, codigoDe(t, err))
	assert.Equal(t, "ngx v0.2.0", conteudo(t, caminho))
}

func TestExecutarSemChavePublicaRecusaAntesDeBaixarQualquerCoisa(t *testing.T) {
	// Binario construido sem -ldflags nao pode se auto-atualizar, e a recusa
	// vem antes de qualquer requisicao: nao ha porque baixar o que nao se
	// pode verificar.
	c := novoCenario(t, "0.3.0", "ngx v0.3.0")
	caminho := binarioDeTeste(t, "ngx v0.2.0", 0o755)

	opts := c.opcoes(caminho, "v0.2.0")
	opts.ChavePublicaOverride = ""
	// A chave do pacote tambem esta vazia por padrao (o par ainda nao existe).
	require.Empty(t, ChavePublica, "ChavePublica deve nascer vazia ate a chave real existir")

	_, err := Executar(context.Background(), opts)

	assert.Equal(t, CodigoSemChavePublica, codigoDe(t, err))
	assert.Empty(t, c.srv.visitados(), "nao deveria ter tocado a rede")
	assert.Equal(t, "ngx v0.2.0", conteudo(t, caminho))
}

func TestExecutarSomenteVerificarNaoPrecisaDeChaveNemTrocaNada(t *testing.T) {
	c := novoCenario(t, "0.3.0", "ngx v0.3.0")
	caminho := binarioDeTeste(t, "ngx v0.2.0", 0o755)

	opts := c.opcoes(caminho, "v0.2.0")
	opts.ChavePublicaOverride = ""
	opts.SomenteVerificar = true

	res, err := Executar(context.Background(), opts)

	require.NoError(t, err)
	assert.True(t, res.Disponivel)
	assert.False(t, res.Atualizado)
	assert.Equal(t, "ngx v0.2.0", conteudo(t, caminho))
}

func TestExecutarSomenteVerificarQuandoJaEstaAtualizado(t *testing.T) {
	c := novoCenario(t, "0.3.0", "ngx v0.3.0")
	caminho := binarioDeTeste(t, "ngx v0.3.0", 0o755)

	opts := c.opcoes(caminho, "v0.3.0")
	opts.SomenteVerificar = true

	res, err := Executar(context.Background(), opts)

	require.NoError(t, err)
	assert.False(t, res.Disponivel)
	assert.False(t, res.Atualizado)
}

func TestExecutarNaoFazDowngradeSemPedido(t *testing.T) {
	// Downgrade e possivel, nunca acidental: se a release do canal for mais
	// antiga que a instalada, o update e no-op.
	c := novoCenario(t, "0.2.0", "ngx v0.2.0")
	caminho := binarioDeTeste(t, "ngx v0.9.0", 0o755)

	res, err := Executar(context.Background(), c.opcoes(caminho, "v0.9.0"))

	require.NoError(t, err)
	assert.False(t, res.Atualizado)
	assert.False(t, res.Disponivel)
	assert.Equal(t, "ngx v0.9.0", conteudo(t, caminho))
}

func TestExecutarFazDowngradeQuandoAVersaoEhPedidaExplicitamente(t *testing.T) {
	c := novoCenario(t, "0.2.0", "ngx v0.2.0")
	caminho := binarioDeTeste(t, "ngx v0.9.0", 0o755)

	opts := c.opcoes(caminho, "v0.9.0")
	opts.Versao = "v0.2.0"

	res, err := Executar(context.Background(), opts)

	require.NoError(t, err)
	assert.True(t, res.Atualizado)
	assert.Equal(t, "ngx v0.2.0", conteudo(t, caminho))
	assert.Contains(t, c.srv.visitados(), "/repos/s0beran0/ngx/releases/tags/v0.2.0")
}

func TestExecutarComVersaoIgualAInstaladaNaoTrocaNada(t *testing.T) {
	c := novoCenario(t, "0.3.0", "ngx v0.3.0 recompilado")
	caminho := binarioDeTeste(t, "ngx v0.3.0", 0o755)

	opts := c.opcoes(caminho, "v0.3.0")
	opts.Versao = "v0.3.0"

	res, err := Executar(context.Background(), opts)

	require.NoError(t, err)
	assert.False(t, res.Atualizado)
	assert.Equal(t, "ngx v0.3.0", conteudo(t, caminho))
}

func TestExecutarNaoAtualizaQuandoJaEstaNaVersaoDoCanal(t *testing.T) {
	c := novoCenario(t, "0.3.0", "ngx v0.3.0 outro build")
	caminho := binarioDeTeste(t, "ngx v0.3.0", 0o755)

	res, err := Executar(context.Background(), c.opcoes(caminho, "v0.3.0"))

	require.NoError(t, err)
	assert.False(t, res.Atualizado)
	assert.Equal(t, "ngx v0.3.0", conteudo(t, caminho))
}

func TestExecutarComVersaoAtualIlegivelAindaAtualiza(t *testing.T) {
	// Build local sem -ldflags tem versao que nao e semver. Travar o update
	// justamente nesse caso seria travar quem mais precisa dele.
	c := novoCenario(t, "0.3.0", "ngx v0.3.0")
	caminho := binarioDeTeste(t, "ngx dev", 0o755)

	res, err := Executar(context.Background(), c.opcoes(caminho, "dev-sem-versao"))

	require.NoError(t, err)
	assert.True(t, res.Atualizado)
}

func TestExecutarNoCanalBetaConsultaAListaDeReleases(t *testing.T) {
	c := novoCenario(t, "0.4.0-rc.1", "ngx v0.4.0-rc.1")
	caminho := binarioDeTeste(t, "ngx v0.3.0", 0o755)

	opts := c.opcoes(caminho, "v0.3.0")
	opts.Canal = ChannelBeta

	res, err := Executar(context.Background(), opts)

	require.NoError(t, err)
	assert.True(t, res.Atualizado)
	assert.Equal(t, ChannelBeta, res.Canal)
	assert.Equal(t, "v0.4.0-rc.1", res.VersaoRemota)
	assert.Contains(t, c.srv.visitados(), "/repos/s0beran0/ngx/releases")
	assert.NotContains(t, c.srv.visitados(), "/repos/s0beran0/ngx/releases/latest")
}

func TestExecutarRecusaCanalDesconhecido(t *testing.T) {
	c := novoCenario(t, "0.3.0", "ngx v0.3.0")
	caminho := binarioDeTeste(t, "ngx v0.2.0", 0o755)

	opts := c.opcoes(caminho, "v0.2.0")
	opts.Canal = Channel("nightly")

	_, err := Executar(context.Background(), opts)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "canal desconhecido")
	assert.Equal(t, "ngx v0.2.0", conteudo(t, caminho))
}

func TestExecutarSemArtefatoParaAPlataformaPreservaOBinario(t *testing.T) {
	c := novoCenario(t, "0.3.0", "ngx v0.3.0")
	caminho := binarioDeTeste(t, "ngx v0.2.0", 0o755)

	opts := c.opcoes(caminho, "v0.2.0")
	opts.SO = "openbsd"

	_, err := Executar(context.Background(), opts)

	assert.Equal(t, CodigoAssetAusente, codigoDe(t, err))
	assert.Equal(t, "ngx v0.2.0", conteudo(t, caminho))
}

func TestExecutarComDownloadInterrompidoPreservaOBinario(t *testing.T) {
	c := novoCenario(t, "0.3.0", "ngx v0.3.0")
	caminho := binarioDeTeste(t, "ngx v0.2.0", 0o755)
	// O artefato deixa de ser servido no meio do caminho.
	c.srv.responde("/dl/"+c.nomeAsset, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	_, err := Executar(context.Background(), c.opcoes(caminho, "v0.2.0"))

	assert.Equal(t, CodigoRede, codigoDe(t, err))
	assert.Equal(t, "ngx v0.2.0", conteudo(t, caminho))
}

func TestExtrairBinarioDoTarGz(t *testing.T) {
	arq := tarGzCom(t, map[string][]byte{"ngx": []byte("executavel"), "LICENSE": []byte("MIT")})

	bin, err := ExtrairBinario("ngx_1_linux_amd64.tar.gz", arq, "linux")

	require.NoError(t, err)
	assert.Equal(t, "executavel", string(bin))
}

func TestExtrairBinarioDoZipDoWindows(t *testing.T) {
	arq := zipCom(t, map[string][]byte{"ngx.exe": []byte("executavel"), "LICENSE": []byte("MIT")})

	bin, err := ExtrairBinario("ngx_1_windows_amd64.zip", arq, "windows")

	require.NoError(t, err)
	assert.Equal(t, "executavel", string(bin))
}

func TestExtrairBinarioSemOExecutavelDentro(t *testing.T) {
	arq := tarGzCom(t, map[string][]byte{"LICENSE": []byte("MIT")})

	_, err := ExtrairBinario("ngx_1_linux_amd64.tar.gz", arq, "linux")

	assert.Equal(t, CodigoArtefatoInvalido, codigoDe(t, err))
}

func TestExtrairBinarioDeFormatoDesconhecido(t *testing.T) {
	_, err := ExtrairBinario("ngx_1_linux_amd64.rar", []byte("x"), "linux")

	assert.Equal(t, CodigoArtefatoInvalido, codigoDe(t, err))
}

func TestMaisNovaSegueSemver(t *testing.T) {
	assert.True(t, maisNova("v0.3.0", "v0.2.9"))
	assert.False(t, maisNova("v0.2.0", "v0.3.0"))
	assert.False(t, maisNova("v0.3.0", "v0.3.0"))
	// Pre-lancamento e anterior ao lancamento de mesma versao.
	assert.False(t, maisNova("v0.3.0-rc.1", "v0.3.0"))
	assert.True(t, maisNova("v0.3.0", "v0.3.0-rc.1"))
	// Versao remota ilegivel nunca conta como mais nova.
	assert.False(t, maisNova("lixo", "v0.1.0"))
}
