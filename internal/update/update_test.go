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

// cenario assembles a complete release served by httptest: artifact,
// checksums.txt and checksums.txt.minisig signed with a pair generated on the
// spot.
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

	// The checksums.txt covers c.artefato; the server delivers c.servido when
	// the scenario asks for tampering. They are two fields precisely because
	// computing the checksum over the tampered bytes would prove the opposite
	// of what is intended.
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

// corrompeArtefato delivers bytes different from the ones covered by the
// signed checksums.txt -- the tampered or corrupted download case.
func corrompeArtefato(t *testing.T) func(*cenario) {
	t.Helper()
	return func(c *cenario) {
		c.servido = tarGzCom(t, map[string][]byte{"ngx": []byte("TAMPERED BINARY")})
		require.NotEqual(t, c.artefato, c.servido,
			"the served artifact has to differ from what the checksums.txt covers")
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
	// The test that matters most in this package: an artifact that does not
	// match the signed checksums.txt must not come near the binary in use.
	// After the failure, the current ngx has to be intact byte for byte, with
	// the same permission, and with no temporary file left in the directory.
	c := novoCenario(t, "0.3.0", "ngx v0.3.0", corrompeArtefato(t))
	caminho := binarioDeTeste(t, "ngx v0.2.0 IN USE", 0o755)
	antes, err := os.Stat(caminho)
	require.NoError(t, err)

	res, err := Executar(context.Background(), c.opcoes(caminho, "v0.2.0"))

	require.Nil(t, res)
	assert.Equal(t, CodigoChecksumDivergente, codigoDe(t, err))
	assert.Equal(t, "ngx v0.2.0 IN USE", conteudo(t, caminho))

	depois, err := os.Stat(caminho)
	require.NoError(t, err)
	assert.Equal(t, antes.Mode().Perm(), depois.Mode().Perm())
	assert.Equal(t, antes.Size(), depois.Size())

	entradas, err := os.ReadDir(filepath.Dir(caminho))
	require.NoError(t, err)
	assert.Len(t, entradas, 1, "the verification failure left junk in the directory")
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
	// A binary built without -ldflags cannot update itself, and the refusal
	// comes before any request: there is no reason to download what cannot be
	// verified.
	c := novoCenario(t, "0.3.0", "ngx v0.3.0")
	caminho := binarioDeTeste(t, "ngx v0.2.0", 0o755)

	opts := c.opcoes(caminho, "v0.2.0")
	opts.ChavePublicaOverride = ""
	// The package key is empty by default as well (the pair does not exist yet).
	require.Empty(t, ChavePublica, "ChavePublica must be born empty until the real key exists")

	_, err := Executar(context.Background(), opts)

	assert.Equal(t, CodigoSemChavePublica, codigoDe(t, err))
	assert.Empty(t, c.srv.visitados(), "it should not have touched the network")
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
	// A downgrade is possible, never accidental: if the channel's release is
	// older than the installed one, the update is a no-op.
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
	c := novoCenario(t, "0.3.0", "ngx v0.3.0 rebuilt")
	caminho := binarioDeTeste(t, "ngx v0.3.0", 0o755)

	opts := c.opcoes(caminho, "v0.3.0")
	opts.Versao = "v0.3.0"

	res, err := Executar(context.Background(), opts)

	require.NoError(t, err)
	assert.False(t, res.Atualizado)
	assert.Equal(t, "ngx v0.3.0", conteudo(t, caminho))
}

func TestExecutarNaoAtualizaQuandoJaEstaNaVersaoDoCanal(t *testing.T) {
	c := novoCenario(t, "0.3.0", "ngx v0.3.0 another build")
	caminho := binarioDeTeste(t, "ngx v0.3.0", 0o755)

	res, err := Executar(context.Background(), c.opcoes(caminho, "v0.3.0"))

	require.NoError(t, err)
	assert.False(t, res.Atualizado)
	assert.Equal(t, "ngx v0.3.0", conteudo(t, caminho))
}

func TestExecutarComVersaoAtualIlegivelAindaAtualiza(t *testing.T) {
	// A local build without -ldflags has a version that is not semver.
	// Blocking the update in exactly that case would block whoever needs it
	// most.
	c := novoCenario(t, "0.3.0", "ngx v0.3.0")
	caminho := binarioDeTeste(t, "ngx dev", 0o755)

	res, err := Executar(context.Background(), c.opcoes(caminho, "dev-no-version"))

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
	assert.Contains(t, err.Error(), "unknown channel")
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
	// The artifact stops being served midway.
	c.srv.responde("/dl/"+c.nomeAsset, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	_, err := Executar(context.Background(), c.opcoes(caminho, "v0.2.0"))

	assert.Equal(t, CodigoRede, codigoDe(t, err))
	assert.Equal(t, "ngx v0.2.0", conteudo(t, caminho))
}

func TestExtrairBinarioDoTarGz(t *testing.T) {
	arq := tarGzCom(t, map[string][]byte{"ngx": []byte("executable"), "LICENSE": []byte("MIT")})

	bin, err := ExtrairBinario("ngx_1_linux_amd64.tar.gz", arq, "linux")

	require.NoError(t, err)
	assert.Equal(t, "executable", string(bin))
}

func TestExtrairBinarioDoZipDoWindows(t *testing.T) {
	arq := zipCom(t, map[string][]byte{"ngx.exe": []byte("executable"), "LICENSE": []byte("MIT")})

	bin, err := ExtrairBinario("ngx_1_windows_amd64.zip", arq, "windows")

	require.NoError(t, err)
	assert.Equal(t, "executable", string(bin))
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
	// A prerelease comes before the release of the same version.
	assert.False(t, maisNova("v0.3.0-rc.1", "v0.3.0"))
	assert.True(t, maisNova("v0.3.0", "v0.3.0-rc.1"))
	// An unreadable remote version never counts as newer.
	assert.False(t, maisNova("junk", "v0.1.0"))
}
