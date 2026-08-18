//go:build !windows

package update

import (
	"os"
	"path/filepath"
)

// aplicar troca o binario em Linux e macOS.
//
// Escrever POR CIMA de um executavel em uso falha com ETXTBSY; renomear POR
// CIMA dele funciona, porque o inode antigo sobrevive enquanto o processo em
// execucao mantiver o descritor aberto. Por isso o novo binario e escrito num
// temporario NO MESMO DIRETORIO — rename nao cruza filesystem — e entra por
// rename, que e atomico: em nenhum instante o caminho fica sem um binario
// completo.
func aplicar(caminho string, novo []byte, perm os.FileMode) error {
	dir := filepath.Dir(caminho)

	tmp, err := os.CreateTemp(dir, ".ngx-update-*")
	if err != nil {
		return erroDeEscrita(err, caminho)
	}
	nomeTmp := tmp.Name()
	tmp.Close()

	if err := escreverArquivo(nomeTmp, novo, perm); err != nil {
		_ = os.Remove(nomeTmp)
		return erroDeEscrita(err, caminho)
	}

	if err := os.Rename(nomeTmp, caminho); err != nil {
		_ = os.Remove(nomeTmp)
		return erroCausa(err, CodigoTrocaFalhou,
			"nao foi possivel colocar o binario novo em %s; o ngx atual continua "+
				"no lugar e funcionando", caminho)
	}
	return nil
}
