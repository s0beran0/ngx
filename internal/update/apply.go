package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// sufixoAntigo e o nome que o binario em uso recebe no Windows enquanto o
// novo toma o lugar dele (DD5). Fica aqui, e nao no arquivo com build tag,
// porque LimparResiduo roda em todos os sistemas.
const sufixoAntigo = ".old"

// permPadrao e a permissao aplicada quando o binario atual nao existe (caso
// de instalacao nova num diretorio vazio). Executavel para todos, gravavel so
// pelo dono.
const permPadrao os.FileMode = 0o755

// Apply substitui o binario em caminho pelo conteudo de novo.
//
// A troca acontece depois da verificacao, nunca antes, e nunca escreve por
// cima do arquivo em uso: em Unix o novo binario vai para um temporario no
// mesmo diretorio e entra por rename; no Windows o atual e renomeado para
// .old e o novo toma o lugar (DD5). Nos dois casos, uma falha no meio deixa
// um ngx funcional em caminho.
func Apply(caminho string, novo []byte) error {
	if len(novo) == 0 {
		return erro(CodigoArtefatoInvalido,
			"o binario novo veio vazio; a substituicao foi abortada e o ngx atual "+
				"continua no lugar")
	}
	perm := permPadrao
	if info, err := os.Stat(caminho); err == nil {
		perm = info.Mode().Perm()
	}
	return aplicar(caminho, novo, perm)
}

// LimparResiduo remove o binario .old deixado por uma atualizacao anterior no
// Windows, onde o executavel em uso nao pode ser apagado (DD5). Deve ser
// chamada na inicializacao do ngx e falha em silencio de proposito: um
// arquivo orfao nao e problema do usuario, e um erro aqui nao tem nada a ver
// com o comando que ele pediu. Sem esta limpeza, cada atualizacao deixa um
// binario orfao no diretorio para sempre.
func LimparResiduo(caminho string) {
	if caminho == "" {
		return
	}
	_ = os.Remove(caminho + sufixoAntigo)
}

// opsArquivo abstrai as operacoes de arquivo da troca por renomeio. Existe
// para que a sequencia do Windows — a parte cujo modo de falha e "usuario
// sem ngx.exe" — seja testavel em qualquer sistema, inclusive com falhas
// injetadas em cada passo.
type opsArquivo struct {
	escrever func(caminho string, dados []byte, perm os.FileMode) error
	renomear func(de, para string) error
	remover  func(caminho string) error
}

func opsReais() opsArquivo {
	return opsArquivo{
		escrever: escreverArquivo,
		renomear: os.Rename,
		remover:  os.Remove,
	}
}

// trocaComRenomeio implementa a sequencia do Windows (DD5), onde o executavel
// em uso pode ser renomeado mas nao deletado nem sobrescrito:
//
//  1. escreve o novo como <caminho>.new;
//  2. renomeia <caminho> para <caminho>.old;
//  3. renomeia <caminho>.new para <caminho>;
//  4. tenta remover o .old e IGNORA a falha — o arquivo ainda esta em uso
//     pelo processo que esta rodando, e a remocao fica para a proxima
//     execucao, via LimparResiduo.
//
// Se o passo 3 falhar depois de o 2 ter dado certo, o .old volta ao lugar.
// Deixar o usuario sem binario e o pior desfecho possivel desta funcao —
// pior que nao atualizar.
func trocaComRenomeio(ops opsArquivo, caminho string, novo []byte, perm os.FileMode) error {
	novoTmp := caminho + ".new"
	antigo := caminho + sufixoAntigo

	// Um .new remanescente de uma tentativa anterior atrapalharia o rename.
	_ = ops.remover(novoTmp)

	if err := ops.escrever(novoTmp, novo, perm); err != nil {
		return erroDeEscrita(err, caminho)
	}

	if err := ops.renomear(caminho, antigo); err != nil {
		_ = ops.remover(novoTmp)
		return erroCausa(err, CodigoTrocaFalhou,
			"nao foi possivel mover o binario atual %s para %s; nada foi trocado e "+
				"o ngx atual continua funcionando", caminho, antigo)
	}

	if err := ops.renomear(novoTmp, caminho); err != nil {
		// Restaura antes de qualquer outra coisa: neste ponto o caminho esta
		// vazio, e e o unico estado em que o usuario ficaria sem ngx.
		if errRestauro := ops.renomear(antigo, caminho); errRestauro != nil {
			_ = ops.remover(novoTmp)
			return erroCausa(err, CodigoTrocaFalhou,
				"a troca do binario falhou no meio e a restauracao tambem: o binario "+
					"anterior esta em %s e precisa ser renomeado de volta para %s a mao",
				antigo, caminho)
		}
		_ = ops.remover(novoTmp)
		return erroCausa(err, CodigoTrocaFalhou,
			"nao foi possivel colocar o binario novo em %s; o anterior foi restaurado "+
				"e continua funcionando", caminho)
	}

	// Falha esperada enquanto o processo atual segue rodando a partir do
	// arquivo renomeado. LimparResiduo cuida disso na proxima execucao.
	_ = ops.remover(antigo)
	return nil
}

func escreverArquivo(caminho string, dados []byte, perm os.FileMode) error {
	f, err := os.OpenFile(caminho, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(dados); err != nil {
		f.Close()
		_ = os.Remove(caminho)
		return err
	}
	// fsync antes do rename: sem ele, um corte de energia entre os dois pode
	// deixar no lugar do binario um arquivo de tamanho certo e conteudo nulo.
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(caminho)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(caminho)
		return err
	}
	// O modo pedido pode ter sido reduzido pelo umask na criacao.
	return os.Chmod(caminho, perm)
}

// erroDeEscrita distingue falta de permissao de qualquer outra falha de IO.
// O ngx nao tenta escalar privilegio sozinho: ele diz qual diretorio precisa
// ser gravavel e para.
func erroDeEscrita(causa error, caminho string) error {
	dir := filepath.Dir(caminho)
	if errors.Is(causa, fs.ErrPermission) {
		return erroCausa(causa, CodigoPermissao,
			"sem permissao para escrever em %s, que e onde o binario do ngx vive. "+
				"A atualizacao precisa de privilegio de escrita nesse diretorio: "+
				"repita o comando com privilegio ou instale o ngx num diretorio do "+
				"seu usuario. Nada foi trocado", dir)
	}
	return erroCausa(causa, CodigoTrocaFalhou,
		"nao foi possivel escrever o binario novo em %s; nada foi trocado e o ngx "+
			"atual continua funcionando", dir)
}
