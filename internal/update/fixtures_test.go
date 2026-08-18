package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"aead.dev/minisign"
	"github.com/stretchr/testify/require"
)

// parDeChaves gera um par minisign no proprio teste. Nenhuma chave fixa fica
// versionada: uma chave de teste embutida no repositorio vira, mais cedo ou
// mais tarde, uma chave que alguem confunde com a de producao.
func parDeChaves(t *testing.T) (minisign.PublicKey, minisign.PrivateKey) {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

// textoDaChave devolve a chave publica no formato de uma linha, que e como
// ela entra no binario via -ldflags.
func textoDaChave(t *testing.T, pub minisign.PublicKey) string {
	t.Helper()
	return pub.String()
}

// checksumsPara monta um checksums.txt no formato do goreleaser: SHA256 em
// hexadecimal, dois espacos, nome do arquivo.
func checksumsPara(arquivos map[string][]byte) []byte {
	var b bytes.Buffer
	// A ordem nao importa para o parser, mas um mapa iterado direto deixaria
	// o fixture instavel entre execucoes.
	for _, nome := range chavesOrdenadas(arquivos) {
		soma := sha256.Sum256(arquivos[nome])
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(soma[:]), nome)
	}
	return b.Bytes()
}

func chavesOrdenadas(m map[string][]byte) []string {
	nomes := make([]string, 0, len(m))
	for k := range m {
		nomes = append(nomes, k)
	}
	for i := 1; i < len(nomes); i++ {
		for j := i; j > 0 && nomes[j] < nomes[j-1]; j-- {
			nomes[j], nomes[j-1] = nomes[j-1], nomes[j]
		}
	}
	return nomes
}

// tarGzCom empacota os arquivos num tar.gz, como o goreleaser faz para Unix.
func tarGzCom(t *testing.T, arquivos map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, nome := range chavesOrdenadas(arquivos) {
		dados := arquivos[nome]
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     nome,
			Mode:     0o755,
			Size:     int64(len(dados)),
			Typeflag: tar.TypeReg,
		}))
		_, err := tw.Write(dados)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// zipCom empacota os arquivos num zip, como o goreleaser faz para Windows.
func zipCom(t *testing.T, arquivos map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, nome := range chavesOrdenadas(arquivos) {
		w, err := zw.Create(nome)
		require.NoError(t, err)
		_, err = w.Write(arquivos[nome])
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
