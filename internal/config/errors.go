package config

import "fmt"

// ClasseRecusa names the reason why ngx refused a configuration. It exists
// for one thing only: the FuzzAlinhamento oracle compares ngx against
// crossplane and needs to tell "deliberate refusal, with a known token shape"
// apart from "over-rejection, which is a bug". Each class below is one
// enumerated, narrow divergence with a unit test of its own -- see
// divergenciasConhecidas in fuzz_test.go. A refusal with no class
// (RecusaCrossplane) is the refusal crossplane itself reported, and is never
// a divergence.
type ClasseRecusa string

const (
	// RecusaCrossplane is the error that came out of the crossplane payload.
	RecusaCrossplane ClasseRecusa = ""

	// RecusaAspaNaoFechada: the source ends inside an open quote.
	RecusaAspaNaoFechada ClasseRecusa = "aspa_nao_fechada"

	// RecusaTokenNoLugarDeDiretiva: some other token showed up where a
	// directive name was expected.
	RecusaTokenNoLugarDeDiretiva ClasseRecusa = "token_no_lugar_de_diretiva"

	// RecusaTokenInesperado: a token out of place at any other position of
	// the matching (argument, block close, comment). It is no known
	// divergence at all: if it shows up in the fuzz, it is an aligner bug.
	RecusaTokenInesperado ClasseRecusa = "token_inesperado"

	// RecusaTerminadorAusente: the directive neither ends in ';' nor opens '{'.
	RecusaTerminadorAusente ClasseRecusa = "terminador_ausente"

	// RecusaTokensSobrando: the tree ran out before the tokens did.
	RecusaTokensSobrando ClasseRecusa = "tokens_sobrando"

	// RecusaFimInesperado: the tokens ran out before the tree did.
	RecusaFimInesperado ClasseRecusa = "fim_inesperado"

	// RecusaExpressaoIfInvalida: "if" with no parenthesized expression.
	RecusaExpressaoIfInvalida ClasseRecusa = "expressao_if_invalida"

	// RecusaAlvoNaoERegular: the path exists and opened, but is not a
	// regular file -- directory, socket, fifo, device.
	RecusaAlvoNaoERegular ClasseRecusa = "alvo_nao_e_arquivo_regular"

	// RecusaPanicoDoCrossplane: crossplane panicked while parsing.
	RecusaPanicoDoCrossplane ClasseRecusa = "panico_do_crossplane"

	// RecusaFalhaDeLeitura: reading one of the configuration files failed
	// midway -- the .conf may well be intact, what failed was the I/O. Not a
	// fuzz divergence: the fuzz reads from memory and never produces it.
	RecusaFalhaDeLeitura ClasseRecusa = "falha_de_leitura"
)

// ParseError is a problem found while reading the configuration, with the
// location preserved so that the diagnostic can point at the exact spot.
//
// Classe and Token are there for the comparison against crossplane in the
// fuzz: Token keeps the raw text of the lexeme that motivated the refusal, so
// that the enumerated divergence matches the exact shape of the token ("{",
// "}") instead of matching the message by substring.
type ParseError struct {
	File    string
	Line    int
	Message string
	Classe  ClasseRecusa
	Token   string
}

// ParseErrors aggregates the problems of a parse. It implements error so it
// can be returned by Parse, and keeps the items reachable through errors.As
// so that the output layer can turn them into located diagnostics.
type ParseErrors []ParseError

// Error sums up the problems: the first item, plus the count of the remaining
// ones when there is more than one. The full detail stays available through
// errors.As for whoever needs the file and line of each item.
func (e ParseErrors) Error() string {
	if len(e) == 0 {
		return "parse failed without detailing the error"
	}

	primeiro := e[0]

	// Line zero means "there is no line", not "line zero": a file that never
	// even opened has no position to offer. Printing `file:0` invents a
	// reference that does not exist and reads like a defect -- and the rule
	// of this project is to omit what is unavailable, never to make it up.
	local := primeiro.File
	if primeiro.Line > 0 {
		local = fmt.Sprintf("%s:%d", primeiro.File, primeiro.Line)
	}
	msg := fmt.Sprintf("%s: %s", local, primeiro.Message)
	if len(e) > 1 {
		msg = fmt.Sprintf("%s (and %d more error(s))", msg, len(e)-1)
	}
	return msg
}
