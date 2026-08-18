package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/update"
)

// UpdateData is the data of the `ngx update` envelope. It mirrors
// update.Resultado so the update package does not need to know the envelope.
type UpdateData = update.Resultado

// newUpdateCmd registers `ngx update`: downloads the newest release of the
// channel, verifies signature and checksum, and swaps the binary itself.
//
// The command does NOT talk to the remote target, and therefore ignores --host
// on purpose: updating ngx is about the machine where ngx runs, and nobody
// expects `ngx --host web1 update` to touch the server's binary -- especially
// because DR3 says nothing is installed there.
func newUpdateCmd(ctx *Context) *cobra.Command {
	var (
		canal    string
		versao   string
		conferir bool
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update ngx itself from the signed releases",
		Long: "Downloads the newest release of the channel, checks the minisign signature " +
			"and the checksum, and only then swaps the binary. A failed verification " +
			"leaves the current ngx intact.",
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
		"release channel: stable (default) or beta, which includes pre-releases")
	cmd.Flags().StringVar(&versao, "version", "",
		"install exactly this version, even one older than the current")
	cmd.Flags().BoolVar(&conferir, "check", false,
		"only report whether a new version exists; download and replace nothing")
	return cmd
}

// canalEscolhido resolves the channel precedence: the flag beats the
// environment variable, which beats the default. NGX_CHANNEL exists because
// install.sh already uses it, and whoever installed from the beta channel
// expects to stay on it without repeating the flag on every update.
func canalEscolhido(ctx *Context, flag string) string {
	if flag != "" {
		return flag
	}
	if ctx.Getenv != nil {
		return ctx.Getenv(update.EnvCanal)
	}
	return ""
}

// erroDeUpdate preserves the typed error from the update package. It already
// carries its own code and message -- rewrapping would erase the distinction
// between "there is no new version", "the signature does not check out" and
// "there was no permission to write", which are three outcomes whoever
// consumes the output needs to tell apart.
func erroDeUpdate(err error) error {
	var tipado *output.Error
	if errors.As(err, &tipado) {
		return tipado
	}
	return output.Internal(err, "%s", err.Error())
}
