package config

import "fmt"

// ParseError e um problema encontrado ao ler a configuracao, com a
// localizacao preservada para que o diagnostico possa apontar o lugar exato.
type ParseError struct {
	File    string
	Line    int
	Message string
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
	msg := fmt.Sprintf("%s:%d: %s", primeiro.File, primeiro.Line, primeiro.Message)
	if len(e) > 1 {
		msg = fmt.Sprintf("%s (e mais %d erro(s))", msg, len(e)-1)
	}
	return msg
}
