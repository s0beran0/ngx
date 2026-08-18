package update

import (
	"testing"

	"aead.dev/minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
)

func codeOf(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	e, ok := err.(*output.Error)
	require.Truef(t, ok, "error %T carries no diagnostic", err)
	return e.Diag.Code
}

func TestVerifyAcceptsCorrectSignatureAndChecksum(t *testing.T) {
	pub, priv := keyPair(t)
	artifact := []byte("ngx binary v9")
	checksums := checksumsFor(map[string][]byte{"ngx_9_linux_amd64.tar.gz": artifact})
	sig := minisign.Sign(priv, checksums)

	err := Verify(artifact, checksums, sig, keyText(t, pub), "ngx_9_linux_amd64.tar.gz")
	assert.NoError(t, err)
}

func TestVerifyAcceptsKeyWithComment(t *testing.T) {
	// The public key stored in a file has two lines, the first one being the
	// untrusted comment. Both shapes need to work, because whoever fills in
	// the -ldflags may copy the whole file.
	pub, priv := keyPair(t)
	withComment, err := pub.MarshalText()
	require.NoError(t, err)

	artifact := []byte("binary")
	checksums := checksumsFor(map[string][]byte{"artifact.tar.gz": artifact})
	sig := minisign.Sign(priv, checksums)

	assert.NoError(t, Verify(artifact, checksums, sig, string(withComment), "artifact.tar.gz"))
}

func TestVerifyRejectsMismatchedChecksum(t *testing.T) {
	pub, priv := keyPair(t)
	checksums := checksumsFor(map[string][]byte{"ngx_linux_amd64.tar.gz": []byte("original")})
	sig := minisign.Sign(priv, checksums)

	err := Verify([]byte("swapped"), checksums, sig, keyText(t, pub), "ngx_linux_amd64.tar.gz")

	assert.Equal(t, CodeChecksumMismatch, codeOf(t, err))
	assert.Contains(t, err.Error(), "ngx_linux_amd64.tar.gz")
}

func TestVerifyRejectsInvalidSignatureBeforeLookingAtChecksum(t *testing.T) {
	// The order is the point of the test: checking a hash against an
	// unauthenticated checksums.txt proves nothing. Here the checksums does
	// not even mention the file -- if the returned error were "missing
	// checksum", verification would have read the content before knowing
	// whether it could trust it.
	otherPub, _ := keyPair(t)
	_, attackerPriv := keyPair(t)

	checksums := []byte("a line that does not contain the artifact\n")
	sig := minisign.Sign(attackerPriv, checksums)

	err := Verify([]byte("anything"), checksums, sig, keyText(t, otherPub), "ngx_linux_amd64.tar.gz")

	assert.Equal(t, CodeInvalidSignature, codeOf(t, err))
}

func TestVerifyRejectsSignatureOfOtherContent(t *testing.T) {
	pub, priv := keyPair(t)
	checksums := checksumsFor(map[string][]byte{"a.tar.gz": []byte("content")})
	sig := minisign.Sign(priv, []byte("some other checksums.txt"))

	err := Verify([]byte("content"), checksums, sig, keyText(t, pub), "a.tar.gz")

	assert.Equal(t, CodeInvalidSignature, codeOf(t, err))
}

func TestVerifyRejectsWithoutPublicKey(t *testing.T) {
	// A local build, without -ldflags: the binary does not know how to verify
	// anything. The only acceptable answer is to refuse; "carry on without
	// verifying" does not exist.
	_, priv := keyPair(t)
	checksums := checksumsFor(map[string][]byte{"a.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, checksums)

	err := Verify([]byte("x"), checksums, sig, "", "a.tar.gz")

	assert.Equal(t, CodeNoPublicKey, codeOf(t, err))
	assert.Contains(t, err.Error(), "without an embedded public verification key")
	assert.Contains(t, err.Error(), "cannot update itself")
}

func TestVerifyRejectsWhitespaceOnlyKey(t *testing.T) {
	_, priv := keyPair(t)
	checksums := checksumsFor(map[string][]byte{"a.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, checksums)

	assert.Equal(t, CodeNoPublicKey,
		codeOf(t, Verify([]byte("x"), checksums, sig, "   \n\t ", "a.tar.gz")))
}

func TestVerifyRejectsPlaceholderKey(t *testing.T) {
	// The placeholder exists precisely to fail with a message of its own
	// instead of becoming an obscure parse error.
	_, priv := keyPair(t)
	checksums := checksumsFor(map[string][]byte{"a.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, checksums)

	err := Verify([]byte("x"), checksums, sig, PublicKeyPlaceholder, "a.tar.gz")

	assert.Equal(t, CodePlaceholderKey, codeOf(t, err))
}

func TestVerifyRejectsMalformedKey(t *testing.T) {
	_, priv := keyPair(t)
	checksums := checksumsFor(map[string][]byte{"a.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, checksums)

	err := Verify([]byte("x"), checksums, sig, "this is not a key", "a.tar.gz")

	assert.Equal(t, CodeInvalidKey, codeOf(t, err))
}

func TestVerifyRejectsFileMissingFromChecksums(t *testing.T) {
	pub, priv := keyPair(t)
	checksums := checksumsFor(map[string][]byte{"other.tar.gz": []byte("x")})
	sig := minisign.Sign(priv, checksums)

	err := Verify([]byte("x"), checksums, sig, keyText(t, pub), "ngx_linux_arm64.tar.gz")

	assert.Equal(t, CodeChecksumMissing, codeOf(t, err))
	assert.Contains(t, err.Error(), "ngx_linux_arm64.tar.gz")
}

func TestVerifyDoesNotAcceptKeyFromAnotherPair(t *testing.T) {
	_, priv := keyPair(t)
	strangePub, _ := keyPair(t)
	artifact := []byte("binary")
	checksums := checksumsFor(map[string][]byte{"a.tar.gz": artifact})
	sig := minisign.Sign(priv, checksums)

	err := Verify(artifact, checksums, sig, keyText(t, strangePub), "a.tar.gz")

	assert.Equal(t, CodeInvalidSignature, codeOf(t, err))
}

func TestChecksumForGoreleaserFormat(t *testing.T) {
	// Format confirmed in the goreleaser source: fmt.Sprintf("%v  %v\n",
	// sha, name) -- two spaces between hash and name.
	checksums := []byte(
		"0000000000000000000000000000000000000000000000000000000000000001  ngx_1_linux_amd64.tar.gz\n" +
			"0000000000000000000000000000000000000000000000000000000000000002  ngx_1_windows_amd64.zip\n")

	sum, ok := checksumFor(checksums, "ngx_1_windows_amd64.zip")
	require.True(t, ok)
	assert.Equal(t, "0000000000000000000000000000000000000000000000000000000000000002", sum)
}

func TestChecksumForToleratesBinaryMarker(t *testing.T) {
	checksums := []byte("0000000000000000000000000000000000000000000000000000000000000003 *ngx.tar.gz\n")
	sum, ok := checksumFor(checksums, "ngx.tar.gz")
	require.True(t, ok)
	assert.Equal(t, "0000000000000000000000000000000000000000000000000000000000000003", sum)
}

func TestChecksumForDoesNotMatchByPrefix(t *testing.T) {
	// "ngx_linux_amd64.tar.gz" must not satisfy a request for
	// "ngx_linux_amd64.tar.gz.sig", nor the other way around.
	checksums := []byte("0000000000000000000000000000000000000000000000000000000000000004  ngx_linux_amd64.tar.gz\n")
	_, ok := checksumFor(checksums, "ngx_linux_amd64.tar")
	assert.False(t, ok)
}

func TestChecksumForRejectsMalformedHash(t *testing.T) {
	checksums := []byte("notahexadecimal  ngx.tar.gz\n")
	_, ok := checksumFor(checksums, "ngx.tar.gz")
	assert.False(t, ok)
}
