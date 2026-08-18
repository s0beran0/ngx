//go:build windows

package update

import "os"

// aplicar troca o binario no Windows.
//
// Aqui o executavel em execucao e travado pelo sistema: sobrescrever e
// deletar falham, renomear funciona. Entao a estrategia do Unix (rename por
// cima) nao serve, e a sequencia e a de DD5, implementada em
// trocaComRenomeio: escreve .new, renomeia o atual para .old, poe o novo no
// lugar e deixa a remocao do .old para a proxima execucao (LimparResiduo).
//
// os.Rename do Go usa MoveFileEx com MOVEFILE_REPLACE_EXISTING, que basta
// para os dois renomeios: nao e preciso chamar a API do Windows direto.
func aplicar(caminho string, novo []byte, perm os.FileMode) error {
	return trocaComRenomeio(opsReais(), caminho, novo, perm)
}
