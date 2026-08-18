package cli

import (
	"fmt"
	"io"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/spf13/cobra"
)

// TestData e o veredito de `nginx -t` no data do envelope.
//
// Os diagnosticos nao estao aqui: eles vao para o envelope, que e onde um
// agente ja sabe procurar por achado localizado e onde a severidade derruba
// o ok. Repeti-los dentro do data criaria duas listas que podem discordar.
type TestData struct {
	// OK vem do codigo de saida do nginx, nao do texto.
	OK bool `json:"ok"`

	// ConfigFile e o arquivo de topo que o nginx testou, quando ele diz
	// qual foi. Omitido quando nao diz — nunca deduzido do -c.
	ConfigFile string `json:"config_file,omitempty"`

	// Raw e a saida original do nginx, preservada para o caso que o parser
	// nao reconheceu. Sem ela, uma saida nova viraria um envelope vazio e
	// quem depura ficaria sem nada.
	Raw string `json:"raw,omitempty"`
}

// RenderHuman escreve a linha que um humano quer ler. Os diagnosticos ja
// foram impressos pelo renderer antes de chegar aqui.
func (d TestData) RenderHuman(w io.Writer) error {
	veredito := "reprovada"
	if d.OK {
		veredito = "aprovada"
	}
	if d.ConfigFile == "" {
		_, err := fmt.Fprintf(w, "configuracao %s\n", veredito)
		return err
	}
	_, err := fmt.Fprintf(w, "configuracao %s: %s\n", veredito, d.ConfigFile)
	return err
}

// newTestCmd registra `ngx test`: `nginx -t` executado pelo runtime do
// contexto, portanto no alvo de --host e com o privilegio de --sudo.
func newTestCmd(ctx *Context) *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Roda `nginx -t` e devolve os diagnosticos estruturados",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			execCtx, cancelar := ctx.contextoDeExecucao(cmd.Context())
			defer cancelar()

			res, err := ctx.NovoRuntime().TestConfig(execCtx)
			if err != nil {
				// Aqui o nginx nao chegou a responder: binario ausente,
				// privilegio negado, transporte caido. Isso e falha de
				// infraestrutura, exit 1, e nao configuracao reprovada.
				return err
			}

			env := ctx.NovoEnvelope("test")
			env.Data = TestData{
				OK:         res.OK,
				ConfigFile: res.ConfigFile,
				Raw:        res.Raw,
			}
			for _, d := range res.Diagnostics {
				env.AddDiagnostic(d)
			}

			if err := ctx.Renderer.Render(env); err != nil {
				return err
			}

			if res.OK {
				return nil
			}

			// Configuracao reprovada e resultado, nao falha: o envelope
			// acima ja saiu com os diagnosticos localizados. O que falta e
			// so o exit 3, e por isso o erro vai embrulhado — um segundo
			// envelope no stdout quebraria quem le a saida como um unico
			// documento JSON.
			return semRerrenderizar(output.InvalidConfig(
				"`nginx -t` reprovou a configuracao em %s", ctx.transporte().Describe()))
		},
	}
}
