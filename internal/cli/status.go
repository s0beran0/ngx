package cli

import (
	"fmt"
	"io"

	"github.com/s0beran0/ngx/internal/runtime"
	"github.com/spf13/cobra"
)

// StatusData e o estado do nginx do alvo: o que o binario diz de si (`nginx
// -V`) e o que se conseguiu apurar do processo.
type StatusData struct {
	Nginx   *runtime.Info `json:"nginx"`
	Process ProcessData   `json:"process"`
}

// ProcessData e o State do runtime sem a lista de diagnosticos, que vive no
// envelope. Os campos mantem a semantica de omissao do runtime: o que nao se
// apurou sai do JSON, nunca sai estimado.
type ProcessData struct {
	// Running e ponteiro porque tem tres estados: rodando, nao rodando e
	// "nao deu para saber" — este ultimo e o campo ausente. Nunca false sem
	// evidencia, que diria que o nginx caiu.
	Running *bool `json:"running,omitempty"`

	// MasterPID sai quando o pidfile pode ser lido e contem um pid.
	MasterPID int `json:"master_pid,omitempty"`

	// PIDFile e o caminho consultado. Vazio quando o `nginx -V` nao declara
	// --pid-path: o default do build nao esta na saida e chutar
	// /run/nginx.pid mandaria o operador olhar o arquivo errado.
	PIDFile string `json:"pid_file,omitempty"`
}

// RenderHuman resume o estado em duas linhas. Campo ausente vira frase
// ausente, nao vira zero.
func (d StatusData) RenderHuman(w io.Writer) error {
	if d.Nginx != nil {
		produto := "nginx"
		if d.Nginx.Flavor != "" {
			produto = d.Nginx.Flavor
		}
		versao := d.Nginx.Version
		if versao == "" {
			versao = "(versao nao informada)"
		}
		if _, err := fmt.Fprintf(w, "%s %s em %s\n", produto, versao, d.Nginx.Binary); err != nil {
			return err
		}
	}

	switch {
	case d.Process.Running == nil:
		_, err := fmt.Fprintln(w, "estado do processo indisponivel")
		return err
	case *d.Process.Running:
		if d.Process.MasterPID > 0 {
			_, err := fmt.Fprintf(w, "master %d rodando\n", d.Process.MasterPID)
			return err
		}
		_, err := fmt.Fprintln(w, "master rodando")
		return err
	default:
		_, err := fmt.Fprintln(w, "master parado")
		return err
	}
}

// newStatusCmd registra `ngx status`: deteccao do binario mais estado do
// processo, no alvo de --host e com o privilegio de --sudo.
func newStatusCmd(ctx *Context) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Mostra o nginx do alvo e o estado do processo",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			execCtx, cancelar := ctx.contextoDeExecucao(cmd.Context())
			defer cancelar()

			rt := ctx.NovoRuntime()

			info, err := rt.Detect(execCtx)
			if err != nil {
				return err
			}

			// O pidfile vem do proprio `nginx -V`. Quando o build nao
			// declara --pid-path, o caminho fica vazio e o State devolve o
			// diagnostico que explica a indisponibilidade, em vez de o ngx
			// procurar num caminho que ele escolheu sozinho.
			estado, err := rt.State(execCtx, info.PIDPath)
			if err != nil {
				return err
			}

			env := ctx.NovoEnvelope("status")
			env.Meta.NginxVersion = info.Version
			env.Data = StatusData{
				Nginx: info,
				Process: ProcessData{
					Running:   estado.Running,
					MasterPID: estado.MasterPID,
					PIDFile:   estado.PIDFile,
				},
			}
			for _, d := range estado.Diagnostics {
				env.AddDiagnostic(d)
			}

			return ctx.Renderer.Render(env)
		},
	}
}
