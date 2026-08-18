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
	require.Truef(t, ok, "error %T carries no diagnostic", err)
	return e.Diag.Code
}

func TestVerifyAceitaAssinaturaEChecksumCorretos(t *testing.T) {
	pub, priv := parDeChaves(t)
	artefato := []byte("ngx binary v9")
	somas := checksumsPara(map[string][]byte{"ngx_9_linux_amd64.tar.gz": artefato})
	sig := minisign.Sign(priv, somas)

	err := Verify(artefato, somas, sig, textoDaChave(t, pub), "ngx_9_linux_amd64.tar.gz")
	assert.NoError(t, err)
}

func TestVerifyAceitaChaveComComentario(t *testing.T) {
	// The public key stored in a file has two lines, the first one being the
	// untrusted comment. Both shapes need to work, because whoever fills in
	// the -ldflags may copy the whole file.
	pub, priv := parDeChaves(t)
	comComentario, err := pub.MarshalText()
	require.NoError(t, err)

	artefato := []byte("binary")
	somas := checksumsPara(map[string][]byte{"artifact.tar.gz": artefato})
	sig := minisign.Sign(priv, somas)

	assert.NoError(t, Verify(artefato, somas, sig, string(comComentario), "artifact.tar.gz"))
}

func TestVerifyRecusaChecksumDivergente(t *testing.T) {
	pub, priv := parDeChaves(t)
	somas := checksumsPara(map[string][]byte{"ngx_linux_amd64.tar.gz": []byte("original")})
	sig := minisign.Sign(priv, somas)

	err := Verify([]byte("swapped"), somas, sig, textoDaChave(t, pub), "ngx_linux_amd64.tar.gz")

	assert.Equal(t, CodigoChecksumDivergente, codigoDe(t, err))
	assert.Contains(t, err.Error(), "ngx_linux_amd64.tar.gz")
}

func TestVerifyRecusaAssinaturaInvalidaAntesDeOlharOChecksum(t *testing.T) {
	// The order is the point of the test: checking a hash against an
	// unauthenticated checksums.txt proves nothing. Here the checksums does
	// not even mention the file -- if the returned error were "missing
	// checksum", verification would have read the content before knowing
	// whether it could trust it.
	pubOutra, _ := parDeChaves(t)
	_, privAtacante := parDeChaves(t)

	somas := []byte("a line that does not contain the artifact\n")
	sig := minisign.Sign(privAtacante, somas)

	err := Verify([]byte("anything"), somas, sig, textoDaChave(t, pubOutra), "ngx_linux_amd64.tar.gz")

	assert.Equal(t, CodigoAssinaturaInvalida, codigoDe(t, err))
}

func TestVerifyRecusaAssinaturaDeOutroConteudo(t *testing.T) {
	pub, priv := parDeChaves(t)
	somas := checksumsPara(map[string][]byte{"a.tar.gz": []byte("content")})
	sig := minisign.Sign(priv, []byte("some other checksums.txt"))

	err := Verify([]byte("content"), somas, sig, textoDaChave(t, pub), "a.tar.gz")

	assert.Equal(t, CodigoAssinaturaInvalida, codigoDe(t, err))
}

func TestVerifyRecusaSemChavePublica(t *testing.T) {
	// A local build, without -ldflags: the binary does not know how to verify
	// anything. The only acceptable answer is to refuse; "carry on without
	// verifying" does not exist.
	_, priv := parDeChaves(t)
	somas := checksumsPara(map[string][]byte{"a.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, somas)

	err := Verify([]byte("x"), somas, sig, "", "a.tar.gz")

	assert.Equal(t, CodigoSemChavePublica, codigoDe(t, err))
	assert.Contains(t, err.Error(), "without an embedded public verification key")
	assert.Contains(t, err.Error(), "cannot update itself")
}

func TestVerifyRecusaSoComEspacosNaChave(t *testing.T) {
	_, priv := parDeChaves(t)
	somas := checksumsPara(map[string][]byte{"a.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, somas)

	assert.Equal(t, CodigoSemChavePublica,
		codigoDe(t, Verify([]byte("x"), somas, sig, "   \n\t ", "a.tar.gz")))
}

func TestVerifyRecusaChavePlaceholder(t *testing.T) {
	// The placeholder exists precisely to fail with a message of its own
	// instead of becoming an obscure parse error.
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

	err := Verify([]byte("x"), somas, sig, "this is not a key", "a.tar.gz")

	assert.Equal(t, CodigoChaveInvalida, codigoDe(t, err))
}

func TestVerifyRecusaArquivoAusenteDoChecksums(t *testing.T) {
	pub, priv := parDeChaves(t)
	somas := checksumsPara(map[string][]byte{"other.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, somas)

	err := Verify([]byte("x"), somas, sig, textoDaChave(t, pub), "ngx_linux_arm64.tar.gz")

	assert.Equal(t, CodigoChecksumAusente, codigoDe(t, err))
	assert.Contains(t, err.Error(), "ngx_linux_arm64.tar.gz")
}

func TestVerifyNaoAceitaChaveDeOutroPar(t *testing.T) {
	_, priv := parDeChaves(t)
	pubEstranha, _ := parDeChaves(t)
	artefato := []byte("binary")
	somas := checksumsPara(map[string][]byte{"a.tar.gz": artefato})
	sig := minisign.Sign(priv, somas)

	err := Verify(artefato, somas, sig, textoDaChave(t, pubEstranha), "a.tar.gz")

	assert.Equal(t, CodigoAssinaturaInvalida, codigoDe(t, err))
}

func TestChecksumDeFormatoDoGoreleaser(t *testing.T) {
	// Format confirmed in the goreleaser source: fmt.Sprintf("%v  %v\n",
	// sha, name) -- two spaces between hash and name.
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
	// "ngx_linux_amd64.tar.gz" must not satisfy a request for
	// "ngx_linux_amd64.tar.gz.sig", nor the other way around.
	somas := []byte("0000000000000000000000000000000000000000000000000000000000000004  ngx_linux_amd64.tar.gz\n")
	_, ok := checksumDe(somas, "ngx_linux_amd64.tar")
	assert.False(t, ok)
}

func TestChecksumDeRecusaHashMalformado(t *testing.T) {
	somas := []byte("notahexadecimal  ngx.tar.gz\n")
	_, ok := checksumDe(somas, "ngx.tar.gz")
	assert.False(t, ok)
}
