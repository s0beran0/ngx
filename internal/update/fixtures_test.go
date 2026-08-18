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

// keyPair generates a minisign pair inside the test itself. No fixed key
// is versioned: a test key embedded in the repository becomes, sooner or
// later, a key someone mistakes for the production one.
func keyPair(t *testing.T) (minisign.PublicKey, minisign.PrivateKey) {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

// keyText returns the public key in the single-line format, which is how
// it goes into the binary via -ldflags.
func keyText(t *testing.T, pub minisign.PublicKey) string {
	t.Helper()
	return pub.String()
}

// checksumsFor assembles a checksums.txt in the goreleaser format: SHA256 in
// hexadecimal, two spaces, the file name.
func checksumsFor(files map[string][]byte) []byte {
	var b bytes.Buffer
	// The order does not matter to the parser, but iterating a map directly
	// would leave the fixture unstable across runs.
	for _, name := range sortedKeys(files) {
		sum := sha256.Sum256(files[name])
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	return b.Bytes()
}

func sortedKeys(m map[string][]byte) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

// tarGzWith packs the files into a tar.gz, the way goreleaser does for Unix.
func tarGzWith(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range sortedKeys(files) {
		data := files[name]
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
		}))
		_, err := tw.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// zipWith packs the files into a zip, the way goreleaser does for Windows.
func zipWith(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range sortedKeys(files) {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(files[name])
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
