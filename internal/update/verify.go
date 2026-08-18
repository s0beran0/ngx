package update

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"

	"aead.dev/minisign"
)

// Verify prova que o artefato baixado e o que o projeto publicou.
//
// A ordem e obrigatoria e nao tem atalho:
//
//  1. a chave publica embutida existe e e legivel;
//  2. a assinatura minisign cobre os bytes do checksums.txt;
//  3. so entao o checksums.txt e lido;
//  4. o SHA256 do artefato bate com a linha do arquivo.
//
// Conferir hash contra um checksums.txt nao autenticado nao prova nada — quem
// consegue publicar um binario adulterado publica o checksum dele junto. Por
// isso o passo 2 vem antes do 3, e uma assinatura invalida recusa sem sequer
// olhar o conteudo do checksums.txt.
func Verify(dados, checksums, assinatura []byte, chavePublica, nomeArquivo string) error {
	if err := validarChave(chavePublica); err != nil {
		return err
	}

	var chave minisign.PublicKey
	if err := chave.UnmarshalText([]byte(strings.TrimSpace(chavePublica))); err != nil {
		return erroCausa(err, CodigoChaveInvalida,
			"a chave publica embutida neste binario nao e uma chave minisign valida, "+
				"entao nao ha como verificar a release baixada")
	}

	if !minisign.Verify(chave, checksums, assinatura) {
		return erro(CodigoAssinaturaInvalida,
			"a assinatura de %s nao confere com a chave publica embutida: os "+
				"arquivos foram baixados de uma origem que nao e o projeto, foram "+
				"adulterados no caminho, ou a release foi assinada com outra chave. "+
				"Nada foi instalado e o ngx atual continua no lugar", NomeChecksums)
	}

	esperado, ok := checksumDe(checksums, nomeArquivo)
	if !ok {
		return erro(CodigoChecksumAusente,
			"o arquivo %s nao aparece em %s, entao o download nao pode ser verificado",
			nomeArquivo, NomeChecksums)
	}

	soma := sha256.Sum256(dados)
	obtido := hex.EncodeToString(soma[:])
	if subtle.ConstantTimeCompare([]byte(obtido), []byte(esperado)) != 1 {
		return erro(CodigoChecksumDivergente,
			"o SHA256 de %s nao bate com o publicado: esperado %s, obtido %s. O "+
				"download veio corrompido ou adulterado; nada foi instalado e o ngx "+
				"atual continua no lugar", nomeArquivo, esperado, obtido)
	}
	return nil
}

// checksumDe procura a linha de nomeArquivo no checksums.txt.
//
// O formato e o do sha256sum, que e o que o goreleaser escreve: hash em
// hexadecimal, DOIS espacos, nome do arquivo ("%v  %v\n"). O prefixo "*" no
// nome, que o sha256sum usa para modo binario, e tolerado porque outras
// ferramentas o produzem.
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
