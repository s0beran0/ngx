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

// parDeChaves generates a minisign pair inside the test itself. No fixed key
// is versioned: a test key embedded in the repository becomes, sooner or
// later, a key someone mistakes for the production one.
func parDeChaves(t *testing.T) (minisign.PublicKey, minisign.PrivateKey) {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

// textoDaChave returns the public key in the single-line format, which is how
// it goes into the binary via -ldflags.
func textoDaChave(t *testing.T, pub minisign.PublicKey) string {
	t.Helper()
	return pub.String()
}

// checksumsPara assembles a checksums.txt in the goreleaser format: SHA256 in
// hexadecimal, two spaces, the file name.
func checksumsPara(arquivos map[string][]byte) []byte {
	var b bytes.Buffer
	// The order does not matter to the parser, but iterating a map directly
	// would leave the fixture unstable across runs.
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

// tarGzCom packs the files into a tar.gz, the way goreleaser does for Unix.
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

// zipCom packs the files into a zip, the way goreleaser does for Windows.
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
