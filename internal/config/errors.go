package config

import "fmt"

// ClasseRecusa nomeia a razao pela qual o ngx recusou uma configuracao. Ela
// existe para uma coisa so: o oraculo do FuzzAlinhamento compara o ngx com o
// crossplane e precisa distinguir "recusa deliberada, com forma de token
// conhecida" de "sobre-rejeicao, que e bug". Cada classe abaixo e uma
// divergencia enumerada, estreita e com teste unitario proprio -- ver
// divergenciasConhecidas em fuzz_test.go. Uma recusa sem classe
// (RecusaCrossplane) e a recusa que o proprio crossplane relatou, e nunca e
// divergencia.
type ClasseRecusa string

const (
	// RecusaCrossplane e o erro que veio do payload do crossplane.
	RecusaCrossplane ClasseRecusa = ""

	// RecusaAspaNaoFechada: a fonte termina dentro de uma aspa aberta.
	RecusaAspaNaoFechada ClasseRecusa = "aspa_nao_fechada"

	// RecusaTokenNoLugarDeDiretiva: onde um nome de diretiva era esperado
	// veio outro token.
	RecusaTokenNoLugarDeDiretiva ClasseRecusa = "token_no_lugar_de_diretiva"

	// RecusaTokenInesperado: token fora de lugar em qualquer outra posicao
	// do casamento (argumento, fecha-bloco, comentario). Nao e divergencia
	// conhecida nenhuma: se aparecer no fuzz, e bug do aligner.
	RecusaTokenInesperado ClasseRecusa = "token_inesperado"

	// RecusaTerminadorAusente: a diretiva nao termina em ';' nem abre '{'.
	RecusaTerminadorAusente ClasseRecusa = "terminador_ausente"

	// RecusaTokensSobrando: a arvore acabou antes dos tokens.
	RecusaTokensSobrando ClasseRecusa = "tokens_sobrando"

	// RecusaFimInesperado: os tokens acabaram antes da arvore.
	RecusaFimInesperado ClasseRecusa = "fim_inesperado"

	// RecusaExpressaoIfInvalida: "if" sem expressao entre parenteses.
	RecusaExpressaoIfInvalida ClasseRecusa = "expressao_if_invalida"

	// RecusaAlvoNaoERegular: o caminho existe, abriu, e nao e arquivo
	// regular -- diretorio, socket, fifo, dispositivo.
	RecusaAlvoNaoERegular ClasseRecusa = "alvo_nao_e_arquivo_regular"

	// RecusaPanicoDoCrossplane: o crossplane entrou em panico ao parsear.
	RecusaPanicoDoCrossplane ClasseRecusa = "panico_do_crossplane"

	// RecusaFalhaDeLeitura: a leitura de um arquivo da configuracao falhou
	// no meio -- o .conf pode estar intacto, quem falhou foi o I/O. Nao e
	// divergencia do fuzz: o fuzz le de memoria e nunca a produz.
	RecusaFalhaDeLeitura ClasseRecusa = "falha_de_leitura"
)

// ParseError e um problema encontrado ao ler a configuracao, com a
// localizacao preservada para que o diagnostico possa apontar o lugar exato.
//
// Classe e Token existem para a comparacao com o crossplane no fuzz: Token
// guarda o texto cru do lexema que motivou a recusa, para que a divergencia
// enumerada case a forma exata do token ("{", "}") em vez de casar a
// mensagem por substring.
type ParseError struct {
	File    string
	Line    int
	Message string
	Classe  ClasseRecusa
	Token   string
}

// ParseErrors agrega os problemas de um parse. Implementa error para poder
// ser devolvido por Parse, e mantem os itens acessiveis via errors.As para
// que a camada de saida os converta em diagnosticos localizados.
type ParseErrors []ParseError

// Error resume os problemas: o primeiro item, mais a contagem dos demais se
// houver mais de um. O detalhe completo continua disponivel via errors.As
// para quem precisar de arquivo e linha de cada item.
func (e ParseErrors) Error() string {
	if len(e) == 0 {
		return "parse falhou sem detalhar o erro"
	}

	primeiro := e[0]

	// Linha zero significa "nao ha linha", nao "linha zero": um arquivo que
	// nem abriu nao tem posicao a oferecer. Imprimir `arquivo:0` inventa uma
	// referencia que nao existe e parece defeito para quem le -- e a regra do
	// projeto e omitir o indisponivel, nunca preenche-lo.
	local := primeiro.File
	if primeiro.Line > 0 {
		local = fmt.Sprintf("%s:%d", primeiro.File, primeiro.Line)
	}
	msg := fmt.Sprintf("%s: %s", local, primeiro.Message)
	if len(e) > 1 {
		msg = fmt.Sprintf("%s (e mais %d erro(s))", msg, len(e)-1)
	}
	return msg
}
