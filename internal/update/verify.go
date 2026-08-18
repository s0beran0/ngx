package update

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"

	"aead.dev/minisign"
)

// Verify proves that the downloaded artifact is what the project published.
//
// The order is mandatory and has no shortcut:
//
//  1. the embedded public key exists and is readable;
//  2. the minisign signature covers the bytes of checksums.txt;
//  3. only then is checksums.txt read;
//  4. the SHA256 of the artifact matches the line in the file.
//
// Checking a hash against an unauthenticated checksums.txt proves nothing --
// whoever can publish a tampered binary publishes its checksum along with it.
// That is why step 2 comes before step 3, and an invalid signature refuses
// without even looking at the contents of checksums.txt.
func Verify(data, checksums, signature []byte, publicKey, fileName string) error {
	if err := validateKey(publicKey); err != nil {
		return err
	}

	var key minisign.PublicKey
	if err := key.UnmarshalText([]byte(strings.TrimSpace(publicKey))); err != nil {
		return wrapError(err, CodigoChaveInvalida,
			"the public key embedded in this binary is not a valid minisign key, "+
				"so there is no way to verify the downloaded release")
	}

	if !minisign.Verify(key, checksums, signature) {
		return newError(CodigoAssinaturaInvalida,
			"the signature of %s does not match the embedded public key: the "+
				"files were downloaded from a source that is not the project, were "+
				"tampered with along the way, or the release was signed with another key. "+
				"Nothing was installed and the current ngx stays in place", ChecksumsName)
	}

	expected, ok := checksumFor(checksums, fileName)
	if !ok {
		return newError(CodigoChecksumAusente,
			"the file %s does not appear in %s, so the download cannot be verified",
			fileName, ChecksumsName)
	}

	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
		return newError(CodigoChecksumDivergente,
			"the SHA256 of %s does not match the published one: expected %s, got %s. The "+
				"download came corrupted or tampered with; nothing was installed and the "+
				"current ngx stays in place", fileName, expected, got)
	}
	return nil
}

// checksumFor looks for the line of fileName in checksums.txt.
//
// The format is the sha256sum one, which is what goreleaser writes: the hash
// in hexadecimal, TWO spaces, the file name ("%v  %v\n"). The "*" prefix on
// the name, which sha256sum uses for binary mode, is tolerated because other
// tools produce it.
func checksumFor(checksums []byte, fileName string) (string, bool) {
	for _, line := range strings.Split(string(checksums), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(strings.Join(fields[1:], " "), "*")
		if name != fileName {
			continue
		}
		sum := strings.ToLower(fields[0])
		if len(sum) != sha256.Size*2 {
			return "", false
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return "", false
		}
		return sum, true
	}
	return "", false
}
