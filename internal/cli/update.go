package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/update"
)

// UpdateData is the data of the `ngx update` envelope. It mirrors
// update.Result so the update package does not need to know the envelope.
type UpdateData = update.Result

// newUpdateCmd registers `ngx update`: downloads the newest release of the
// channel, verifies signature and checksum, and swaps the binary itself.
//
// The command does NOT talk to the remote target, and therefore ignores --host
// on purpose: updating ngx is about the machine where ngx runs, and nobody
// expects `ngx --host web1 update` to touch the server's binary -- especially
// because DR3 says nothing is installed there.
func newUpdateCmd(ctx *Context) *cobra.Command {
	var (
		channel  string
		versao   string
		conferir bool
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update ngx itself from the signed releases",
		Long: "Downloads the newest release of the channel, checks the minisign signature " +
			"and the checksum, and only then swaps the binary. A failed verification " +
			"leaves the current ngx intact.\n\n" +
			"It updates ngx on the machine where ngx runs, so --host is ignored: nothing " +
			"is installed on the remote target. A binary owned by a package manager refuses " +
			"to replace itself and names the command to use instead; " +
			"`ngx --field data.install_channel version` says which case this is.",
		Example: `  # is there a newer ngx? (downloads and replaces nothing)
  ngx update --check

  # update
  ngx update

  # follow the pre-releases instead
  ngx update --channel beta

  # go back to a known version
  ngx update --version 0.1.0`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			execCtx, cancel := ctx.executionContext(cmd.Context())
			defer cancel()

			c, err := update.ParseChannel(chosenChannel(ctx, channel))
			if err != nil {
				return output.Usage("%s", err.Error())
			}

			res, err := update.Run(execCtx, update.Options{
				Channel:        c,
				Version:        versao,
				CurrentVersion: output.Version,
				CheckOnly:      conferir,
			})
			if err != nil {
				return updateFailure(err)
			}

			env := ctx.NewEnvelope("update")
			env.Data = res
			return ctx.Renderer.Render(env)
		},
	}

	cmd.Flags().StringVar(&channel, "channel", "",
		"release channel: stable (default) or beta, which includes pre-releases")
	cmd.Flags().StringVar(&versao, "version", "",
		"install exactly this version, even one older than the current")
	cmd.Flags().BoolVar(&conferir, "check", false,
		"only report whether a new version exists; download and replace nothing")
	return cmd
}

// chosenChannel resolves the channel precedence: the flag beats the
// environment variable, which beats the default. NGX_CHANNEL exists because
// install.sh already uses it, and whoever installed from the beta channel
// expects to stay on it without repeating the flag on every update.
func chosenChannel(ctx *Context, flag string) string {
	if flag != "" {
		return flag
	}
	if ctx.Getenv != nil {
		return ctx.Getenv(update.EnvChannel)
	}
	return ""
}

// updateFailure preserves the typed error from the update package. It already
// carries its own code and message -- rewrapping would erase the distinction
// between "there is no new version", "the signature does not check out" and
// "there was no permission to write", which are three outcomes whoever
// consumes the output needs to tell apart.
func updateFailure(err error) error {
	var typed *output.Error
	if errors.As(err, &typed) {
		return typed
	}
	return output.Internal(err, "%s", err.Error())
}
