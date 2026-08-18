package cli

import (
	"fmt"
	"io"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/spf13/cobra"
)

// TestData is the verdict of `nginx -t` in the envelope's data.
//
// The diagnostics are not here: they go into the envelope, which is where an
// agent already knows to look for a located finding and where severity brings
// ok down. Repeating them inside data would create two lists that can
// disagree.
type TestData struct {
	// OK comes from nginx's exit code, not from the text.
	OK bool `json:"ok"`

	// ConfigFile is the top-level file nginx tested, when it says which one
	// it was. Omitted when it does not say — never inferred from -c.
	ConfigFile string `json:"config_file,omitempty"`

	// Raw is nginx's original output, kept for the case the parser did not
	// recognize. Without it, a new output format would become an empty
	// envelope and whoever is debugging would be left with nothing.
	Raw string `json:"raw,omitempty"`
}

// RenderHuman writes the line a human wants to read. The diagnostics were
// already printed by the renderer before getting here.
func (d TestData) RenderHuman(w io.Writer) error {
	verdict := "rejected"
	if d.OK {
		verdict = "accepted"
	}
	if d.ConfigFile == "" {
		_, err := fmt.Fprintf(w, "configuration %s\n", verdict)
		return err
	}
	_, err := fmt.Fprintf(w, "configuration %s: %s\n", verdict, d.ConfigFile)
	return err
}

// newTestCmd registers `ngx test`: `nginx -t` run by the context's runtime,
// therefore on the --host target and with the privilege of --sudo.
func newTestCmd(ctx *Context) *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Run `nginx -t` and return structured diagnostics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			execCtx, cancel := ctx.executionContext(cmd.Context())
			defer cancel()

			res, err := ctx.NewRuntime().TestConfig(execCtx)
			if err != nil {
				// Here nginx never got to answer: missing binary, denied
				// privilege, transport down. That is an infrastructure
				// failure, exit 1, and not a rejected configuration.
				return err
			}

			env := ctx.NewEnvelope("test")
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

			// A rejected configuration is a result, not a failure: the
			// envelope above already went out with the located diagnostics.
			// All that is missing is exit 3, which is why the error goes
			// wrapped — a second envelope on stdout would break whoever reads
			// the output as a single JSON document.
			return withoutRerender(output.InvalidConfig(
				"`nginx -t` rejected the configuration on %s", ctx.activeTransport().Describe()))
		},
	}
}
