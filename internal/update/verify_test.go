package update

import (
	"testing"

	"aead.dev/minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
)

func codigoDe(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	e, ok := err.(*output.Error)
	require.Truef(t, ok, "erro %T nao carrega diagnostico", err)
	return e.Diag.Code
}

func TestVerifyAceitaAssinaturaEChecksumCorretos(t *testing.T) {
	pub, priv := parDeChaves(t)
	artefato := []byte("binario do ngx v9")
	somas := checksumsPara(map[string][]byte{"ngx_9_linux_amd64.tar.gz": artefato})
	sig := minisign.Sign(priv, somas)

	err := Verify(artefato, somas, sig, textoDaChave(t, pub), "ngx_9_linux_amd64.tar.gz")
	assert.NoError(t, err)
}

func TestVerifyAceitaChaveComComentario(t *testing.T) {
	// A chave publica em arquivo tem duas linhas, a primeira sendo o
	// comentario nao confiavel. As duas formas precisam funcionar, porque a
	// pessoa que preencher o -ldflags pode copiar o arquivo inteiro.
	pub, priv := parDeChaves(t)
	comComentario, err := pub.MarshalText()
	require.NoError(t, err)

	artefato := []byte("binario")
	somas := checksumsPara(map[string][]byte{"artefato.tar.gz": artefato})
	sig := minisign.Sign(priv, somas)

	assert.NoError(t, Verify(artefato, somas, sig, string(comComentario), "artefato.tar.gz"))
}

func TestVerifyRecusaChecksumDivergente(t *testing.T) {
	pub, priv := parDeChaves(t)
	somas := checksumsPara(map[string][]byte{"ngx_linux_amd64.tar.gz": []byte("original")})
	sig := minisign.Sign(priv, somas)

	err := Verify([]byte("trocado"), somas, sig, textoDaChave(t, pub), "ngx_linux_amd64.tar.gz")

	assert.Equal(t, CodigoChecksumDivergente, codigoDe(t, err))
	assert.Contains(t, err.Error(), "ngx_linux_amd64.tar.gz")
}

func TestVerifyRecusaAssinaturaInvalidaAntesDeOlharOChecksum(t *testing.T) {
	// A ordem e o ponto do teste: conferir hash contra um checksums.txt nao
	// autenticado nao prova nada. Aqui o checksums nem sequer menciona o
	// arquivo — se o erro devolvido fosse "checksum ausente", a verificacao
	// teria lido o conteudo antes de saber se podia confiar nele.
	pubOutra, _ := parDeChaves(t)
	_, privAtacante := parDeChaves(t)

	somas := []byte("linha que nao contem o artefato\n")
	sig := minisign.Sign(privAtacante, somas)

	err := Verify([]byte("qualquer"), somas, sig, textoDaChave(t, pubOutra), "ngx_linux_amd64.tar.gz")

	assert.Equal(t, CodigoAssinaturaInvalida, codigoDe(t, err))
}

func TestVerifyRecusaAssinaturaDeOutroConteudo(t *testing.T) {
	pub, priv := parDeChaves(t)
	somas := checksumsPara(map[string][]byte{"a.tar.gz": []byte("conteudo")})
	sig := minisign.Sign(priv, []byte("outro checksums.txt qualquer"))

	err := Verify([]byte("conteudo"), somas, sig, textoDaChave(t, pub), "a.tar.gz")

	assert.Equal(t, CodigoAssinaturaInvalida, codigoDe(t, err))
}

func TestVerifyRecusaSemChavePublica(t *testing.T) {
	// Build local, sem -ldflags: o binario nao sabe verificar nada. A unica
	// resposta aceitavel e recusar; "seguir sem verificar" nao existe.
	_, priv := parDeChaves(t)
	somas := checksumsPara(map[string][]byte{"a.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, somas)

	err := Verify([]byte("x"), somas, sig, "", "a.tar.gz")

	assert.Equal(t, CodigoSemChavePublica, codigoDe(t, err))
	assert.Contains(t, err.Error(), "sem chave publica")
	assert.Contains(t, err.Error(), "nao pode se auto-atualizar")
}

func TestVerifyRecusaSoComEspacosNaChave(t *testing.T) {
	_, priv := parDeChaves(t)
	somas := checksumsPara(map[string][]byte{"a.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, somas)

	assert.Equal(t, CodigoSemChavePublica,
		codigoDe(t, Verify([]byte("x"), somas, sig, "   \n\t ", "a.tar.gz")))
}

func TestVerifyRecusaChavePlaceholder(t *testing.T) {
	// O placeholder existe justamente para falhar com mensagem propria em vez
	// de virar um erro de parse obscuro.
	_, priv := parDeChaves(t)
	somas := checksumsPara(map[string][]byte{"a.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, somas)

	err := Verify([]byte("x"), somas, sig, PlaceholderChavePublica, "a.tar.gz")

	assert.Equal(t, CodigoChavePlaceholder, codigoDe(t, err))
}

func TestVerifyRecusaChaveMalformada(t *testing.T) {
	_, priv := parDeChaves(t)
	somas := checksumsPara(map[string][]byte{"a.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, somas)

	err := Verify([]byte("x"), somas, sig, "isto nao e uma chave", "a.tar.gz")

	assert.Equal(t, CodigoChaveInvalida, codigoDe(t, err))
}

func TestVerifyRecusaArquivoAusenteDoChecksums(t *testing.T) {
	pub, priv := parDeChaves(t)
	somas := checksumsPara(map[string][]byte{"outro.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, somas)

	err := Verify([]byte("x"), somas, sig, textoDaChave(t, pub), "ngx_linux_arm64.tar.gz")

	assert.Equal(t, CodigoChecksumAusente, codigoDe(t, err))
	assert.Contains(t, err.Error(), "ngx_linux_arm64.tar.gz")
}

func TestVerifyNaoAceitaChaveDeOutroPar(t *testing.T) {
	_, priv := parDeChaves(t)
	pubEstranha, _ := parDeChaves(t)
	artefato := []byte("binario")
	somas := checksumsPara(map[string][]byte{"a.tar.gz": artefato})
	sig := minisign.Sign(priv, somas)

	err := Verify(artefato, somas, sig, textoDaChave(t, pubEstranha), "a.tar.gz")

	assert.Equal(t, CodigoAssinaturaInvalida, codigoDe(t, err))
}

func TestChecksumDeFormatoDoGoreleaser(t *testing.T) {
	// Formato confirmado no fonte do goreleaser: fmt.Sprintf("%v  %v\n",
	// sha, nome) — dois espacos entre hash e nome.
	somas := []byte(
		"0000000000000000000000000000000000000000000000000000000000000001  ngx_1_linux_amd64.tar.gz\n" +
			"0000000000000000000000000000000000000000000000000000000000000002  ngx_1_windows_amd64.zip\n")

	soma, ok := checksumDe(somas, "ngx_1_windows_amd64.zip")
	require.True(t, ok)
	assert.Equal(t, "0000000000000000000000000000000000000000000000000000000000000002", soma)
}

func TestChecksumDeToleraMarcadorBinario(t *testing.T) {
	somas := []byte("0000000000000000000000000000000000000000000000000000000000000003 *ngx.tar.gz\n")
	soma, ok := checksumDe(somas, "ngx.tar.gz")
	require.True(t, ok)
	assert.Equal(t, "0000000000000000000000000000000000000000000000000000000000000003", soma)
}

func TestChecksumDeNaoCasaPorPrefixo(t *testing.T) {
	// "ngx_linux_amd64.tar.gz" nao pode satisfazer um pedido por
	// "ngx_linux_amd64.tar.gz.sig" nem vice-versa.
	somas := []byte("0000000000000000000000000000000000000000000000000000000000000004  ngx_linux_amd64.tar.gz\n")
	_, ok := checksumDe(somas, "ngx_linux_amd64.tar")
	assert.False(t, ok)
}

func TestChecksumDeRecusaHashMalformado(t *testing.T) {
	somas := []byte("naoehexadecimal  ngx.tar.gz\n")
	_, ok := checksumDe(somas, "ngx.tar.gz")
	assert.False(t, ok)
}
