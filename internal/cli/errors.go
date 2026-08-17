package cli

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
