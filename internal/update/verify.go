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
func Verify(dados, checksums, assinatura []byte, chavePublica, nomeArquivo string) error {
	if err := validarChave(chavePublica); err != nil {
		return err
	}

	var chave minisign.PublicKey
	if err := chave.UnmarshalText([]byte(strings.TrimSpace(chavePublica))); err != nil {
		return erroCausa(err, CodigoChaveInvalida,
			"the public key embedded in this binary is not a valid minisign key, "+
				"so there is no way to verify the downloaded release")
	}

	if !minisign.Verify(chave, checksums, assinatura) {
		return erro(CodigoAssinaturaInvalida,
			"the signature of %s does not match the embedded public key: the "+
				"files were downloaded from a source that is not the project, were "+
				"tampered with along the way, or the release was signed with another key. "+
				"Nothing was installed and the current ngx stays in place", NomeChecksums)
	}

	esperado, ok := checksumDe(checksums, nomeArquivo)
	if !ok {
		return erro(CodigoChecksumAusente,
			"the file %s does not appear in %s, so the download cannot be verified",
			nomeArquivo, NomeChecksums)
	}

	soma := sha256.Sum256(dados)
	obtido := hex.EncodeToString(soma[:])
	if subtle.ConstantTimeCompare([]byte(obtido), []byte(esperado)) != 1 {
		return erro(CodigoChecksumDivergente,
			"the SHA256 of %s does not match the published one: expected %s, got %s. The "+
				"download came corrupted or tampered with; nothing was installed and the "+
				"current ngx stays in place", nomeArquivo, esperado, obtido)
	}
	return nil
}

// checksumDe looks for the line of nomeArquivo in checksums.txt.
//
// The format is the sha256sum one, which is what goreleaser writes: the hash
// in hexadecimal, TWO spaces, the file name ("%v  %v\n"). The "*" prefix on
// the name, which sha256sum uses for binary mode, is tolerated because other
// tools produce it.
func checksumDe(checksums []byte, nomeArquivo string) (string, bool) {
	for _, linha := range strings.Split(string(checksums), "\n") {
		linha = strings.TrimSpace(linha)
		if linha == "" || strings.HasPrefix(linha, "#") {
			continue
		}
		campos := strings.Fields(linha)
		if len(campos) < 2 {
			continue
		}
		nome := strings.TrimPrefix(strings.Join(campos[1:], " "), "*")
		if nome != nomeArquivo {
			continue
		}
		soma := strings.ToLower(campos[0])
		if len(soma) != sha256.Size*2 {
			return "", false
		}
		if _, err := hex.DecodeString(soma); err != nil {
			return "", false
		}
		return soma, true
	}
	return "", false
}
