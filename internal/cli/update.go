package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/update"
)

// UpdateData e o data do envelope de `ngx update`. Espelha update.Resultado
// para que o pacote de atualizacao nao precise conhecer o envelope.
type UpdateData = update.Resultado

// newUpdateCmd registra `ngx update`: baixa a release mais nova do canal,
// verifica assinatura e checksum, e troca o proprio binario.
//
// O comando NAO fala com o alvo remoto, e por isso ignora --host de
// proposito: atualizar o ngx e sobre a maquina onde o ngx roda, e ninguem
// espera que `ngx --host web1 update` mexa no binario do servidor -- ate
// porque a DR3 diz que nada e instalado la.
func newUpdateCmd(ctx *Context) *cobra.Command {
	var (
		canal    string
		versao   string
		conferir bool
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Atualiza o proprio ngx a partir das releases assinadas",
		Long: "Baixa a release mais nova do canal, confere a assinatura minisign e o " +
			"checksum, e so entao troca o binario. Falha de verificacao deixa o ngx " +
			"atual intacto.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			execCtx, cancelar := ctx.contextoDeExecucao(cmd.Context())
			defer cancelar()

			c, err := update.ParseChannel(canalEscolhido(ctx, canal))
			if err != nil {
				return output.Usage("%s", err.Error())
			}

			res, err := update.Executar(execCtx, update.Opcoes{
				Canal:            c,
				Versao:           versao,
				VersaoAtual:      output.Version,
				SomenteVerificar: conferir,
			})
			if err != nil {
				return erroDeUpdate(err)
			}

			env := ctx.NovoEnvelope("update")
			env.Data = res
			return ctx.Renderer.Render(env)
		},
	}

	cmd.Flags().StringVar(&canal, "channel", "",
		"canal de release: stable (padrao) ou beta, que inclui pre-lancamentos")
	cmd.Flags().StringVar(&versao, "version", "",
		"instala exatamente esta versao, inclusive mais antiga que a atual")
	cmd.Flags().BoolVar(&conferir, "check", false,
		"so informa se ha versao nova; nao baixa nem troca nada")
	return cmd
}

// canalEscolhido resolve a precedencia do canal: a flag vence a variavel de
// ambiente, que vence o default. NGX_CHANNEL existe porque o install.sh ja a
// usa, e quem instalou pelo canal beta espera continuar nele sem repetir a
// flag a cada atualizacao.
func canalEscolhido(ctx *Context, flag string) string {
	if flag != "" {
		return flag
	}
	if ctx.Getenv != nil {
		return ctx.Getenv(update.EnvCanal)
	}
	return ""
}

// erroDeUpdate preserva o erro tipado do pacote de atualizacao. Ele ja carrega
// codigo e mensagem proprios -- reembrulhar apagaria a distincao entre "nao ha
// versao nova", "a assinatura nao confere" e "faltou permissao para escrever",
// que sao tres desfechos que quem consome a saida precisa separar.
func erroDeUpdate(err error) error {
	var tipado *output.Error
	if errors.As(err, &tipado) {
		return tipado
	}
	return output.Internal(err, "%s", err.Error())
}
