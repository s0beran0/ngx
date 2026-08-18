package cli

import "github.com/s0beran0/ngx/internal/output"

// comandoDe devolve o nome do comando que estava executando, para que o
// envelope de erro identifique a operacao que falhou. Antes de o cobra
// resolver o comando — flag global invalida, por exemplo — nao ha nome, e o
// fallback e o proprio binario.
func comandoDe(ctx *Context) string {
	if ctx == nil || ctx.Command == "" {
		return "ngx"
	}
	return ctx.Command
}

// erroJaRenderizado carrega o exit code de um comando que ja escreveu o
// proprio envelope.
//
// Existe por causa de um caso que nao e erro: `nginx -t` reprovar a
// configuracao e a resposta a pergunta que se fez, e a resposta e o envelope
// completo, com os diagnosticos localizados. Falta so o codigo de saida 3.
// Sem este embrulho, executar renderizaria um segundo envelope — o de erro —
// e o stdout deixaria de ser um unico documento JSON, que e o contrato com
// quem consome.
//
// Unwrap devolve o *output.Error interno para que errors.As e output.CodeOf
// continuem enxergando o codigo; o campo e explicito, e nao embutido, porque
// um *output.Error embutido promoveria o Unwrap dele e a cadeia pularia
// justamente o erro tipado.
type erroJaRenderizado struct {
	err *output.Error
}

func (e *erroJaRenderizado) Error() string { return e.err.Error() }

func (e *erroJaRenderizado) Unwrap() error { return e.err }

// semRerrenderizar marca um erro tipado como ja apresentado ao usuario.
func semRerrenderizar(err *output.Error) error {
	return &erroJaRenderizado{err: err}
}
