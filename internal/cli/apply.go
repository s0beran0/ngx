package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/s0beran0/ngx/internal/apply"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/plan"
	"github.com/s0beran0/ngx/internal/transport"
)

// ApplyData is what `ngx apply` returns.
type ApplyData struct {
	*apply.Result

	// Tested is what `nginx -t` said about the result. Always present on a
	// successful apply, because the apply only succeeded if it passed.
	Tested string `json:"tested,omitempty"`
}

func newApplyCmd(ctx *Context) *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "apply [plan.json]",
		Short: "Apply a plan, validate with nginx, and roll back if it is refused",
		Long: `Reads a plan, checks it still describes this configuration, writes it, runs
` + "`nginx -t`" + `, and puts everything back if nginx refuses.

The plan comes from a file, or from stdin when the argument is "-" or absent.

Nothing is written until every edit has been checked against the bytes on disk,
so the common failure -- a plan built against a configuration that has since
changed -- costs nothing and exits 9. Once writing starts, the original content
of every file is held, so a refusal by nginx is reversible: exit 3, with the
files back as they were.

The one state worth knowing about is data.not_restored. It lists files that were
written and could NOT be put back, which means the configuration on disk is
neither the old one nor a validated new one. It is named rather than counted,
because that list is what an operator has to act on.

A file this process cannot write is written with sudo, but only after the
ordinary write is refused -- never speculatively. See docs/install-channels.md
for what granting that sudo actually costs.`,
		Example: `  # the usual pipeline
  ngx set -c /etc/nginx/nginx.conf --ref conf.d/site.conf#s0.d0 --value 8443 > plan.json
  ngx apply plan.json

  # or in one line
  ngx set ... | ngx apply -

  # check the plan is still valid without writing
  ngx apply --dry-run plan.json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := readPlan(cmd, args)
			if err != nil {
				return err
			}

			tree, root, err := loadTree(ctx, cmd)
			if err != nil {
				return err
			}

			if dryRun {
				if verr := p.Verify(tree, root); verr != nil {
					return applyRefusal(verr)
				}
				env := ctx.NewEnvelope("apply")
				env.Data = ApplyData{Result: &apply.Result{}}
				env.AddDiagnostic(output.Diagnostic{
					Severity: output.SeverityInfo,
					Code:     "NGX-0320",
					Message: fmt.Sprintf("the plan still describes this configuration: %d edit(s), "+
						"%d file(s) created, %d deleted. Nothing was written",
						len(p.Edits), len(p.Creates), len(p.Deletes)),
				})
				return ctx.Renderer.Render(env)
			}

			execCtx, cancel := ctx.executionContext(cmd.Context())
			defer cancel()

			res, applyErr := apply.Run(apply.Options{
				Plan:       p,
				Tree:       tree,
				Root:       root,
				Validate:   nginxValidator(ctx, execCtx, root),
				Privileged: elevatorFor(ctx),
			})

			env := ctx.NewEnvelope("apply")
			env.Data = ApplyData{Result: res}
			for _, path := range res.NotRestored {
				env.AddDiagnostic(output.Diagnostic{
					Severity: output.SeverityError,
					Code:     "NGX-0321",
					File:     path,
					Message: "this file was written and could not be put back. It holds neither " +
						"the old configuration nor a validated new one, and it needs a human",
				})
			}
			if applyErr != nil {
				env.OK = false
				env.AddDiagnostic(output.Diagnostic{
					Severity: output.SeverityError,
					Code:     applyDiagnosticCode(applyErr),
					Message:  applyErr.Error(),
				})
			}
			if renderErr := ctx.Renderer.Render(env); renderErr != nil {
				return renderErr
			}
			if applyErr != nil {
				return withoutRerender(applyRefusal(applyErr))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"check that the plan still describes this configuration, and write nothing")
	return cmd
}

// readPlan takes the plan from a file or from stdin.
func readPlan(cmd *cobra.Command, args []string) (*plan.Plan, error) {
	var raw []byte
	var err error

	switch {
	case len(args) == 0 || args[0] == "-":
		raw, err = io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, output.Usage("could not read the plan from stdin: %s", err.Error())
		}
	default:
		raw, err = os.ReadFile(args[0])
		if err != nil {
			return nil, output.Usage("could not read %s: %s", args[0], err.Error())
		}
	}

	// A plan arrives inside an envelope, because that is what produced it. It
	// is also accepted bare, so a plan edited by hand or built by another tool
	// does not have to be wrapped in something it did not come from.
	var envelope struct {
		Data struct {
			Plan *plan.Plan `json:"plan"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Data.Plan != nil {
		return envelope.Data.Plan, nil
	}

	var bare plan.Plan
	if err := json.Unmarshal(raw, &bare); err != nil {
		return nil, output.Usage("this is not a plan: %s", err.Error())
	}
	if bare.Root == "" {
		return nil, output.Usage(
			"this is not a plan: it names no root configuration. A plan comes from " +
				"`ngx set`, `ngx add`, `ngx rm` or `ngx create`")
	}
	return &bare, nil
}

// nginxValidator runs the real `nginx -t` after the write.
//
// It is the binary and not the parser, because the question is whether NGINX
// accepts the result, and a parser agreeing with itself does not answer it.
func nginxValidator(ctx *Context, execCtx context.Context, root string) apply.Validate {
	return func() error {
		// -c root, not a bare -t: apply writes to the configuration named by
		// -c, and a bare test would ask nginx about whatever it loads by
		// default. On a server the two coincide; against a copy or a staging
		// tree they do not, and the validation would pass while the file just
		// written is broken.
		res, err := ctx.NewRuntime().TestConfigAt(execCtx, root)
		if err != nil {
			return err
		}
		if !res.OK {
			return fmt.Errorf("nginx refused the configuration:\n%s", res.Raw)
		}
		return nil
	}
}

// elevatorFor returns the privileged writer when --sudo was asked for, and nil
// otherwise.
//
// Nil is the default and it matters: escalating because it happens to be
// possible is not a decision this tool makes for somebody. --sudo is the
// operator saying so.
func elevatorFor(ctx *Context) apply.PrivilegedWriter {
	if ctx.Flags == nil || !ctx.Flags.Sudo {
		return nil
	}
	writer, err := transport.NewPrivilegedWriter(ctx.activeTransport())
	if err != nil {
		// The transport cannot feed stdin, which today means it is remote.
		// Returning nil lets the apply proceed unprivileged and fail with the
		// permission error, which names the real problem better than a
		// capability error would.
		return nil
	}
	return writer
}

// applyRefusal maps a failure to an exit code by its class.
func applyRefusal(err error) *output.Error {
	// The plan's own refusals come first, including when they are wrapped in an
	// apply failure: what the operator has to do about a stale hash does not
	// change because it was noticed one layer up.
	if code, ok := plan.CodeOf(err); ok {
		switch code {
		case plan.RefusalStaleHash, plan.RefusalBytesMoved:
			// The configuration changed under the plan. Exit 9 has been
			// reserved for exactly this since v0.1, and this is its first use.
			return output.ConfigChanged("%s", err.Error())
		default:
			// A wrong root or a malformed plan is the caller handing over the
			// wrong document, not the world moving.
			return output.Usage("%s", err.Error())
		}
	}

	code, ok := apply.CodeOf(err)
	if !ok {
		return output.Internal(err, "%s", err.Error())
	}
	switch code {
	case apply.CodeVerifyFailed:
		return output.Usage("%s", err.Error())
	case apply.CodeValidateFailed:
		// nginx refused the result and everything was put back: the same class
		// as `ngx test` failing, which is exit 3.
		return output.InvalidConfig("%s", err.Error())
	default:
		// A write that did not land, or a rollback that failed. Neither is the
		// caller's mistake.
		return output.Internal(err, "%s", err.Error())
	}
}

func applyDiagnosticCode(err error) string {
	code, ok := apply.CodeOf(err)
	if !ok {
		return "NGX-0329"
	}
	switch code {
	case apply.CodeVerifyFailed:
		return "NGX-0322"
	case apply.CodeWriteFailed:
		return "NGX-0323"
	case apply.CodeValidateFailed:
		return "NGX-0324"
	case apply.CodeRollbackFailed:
		return "NGX-0325"
	default:
		return "NGX-0329"
	}
}

// ReloadData is what `ngx reload` returns.
type ReloadData struct {
	Reloaded bool   `json:"reloaded"`
	Tested   bool   `json:"tested_ok"`
	Raw      string `json:"raw,omitempty"`
}

func newReloadCmd(ctx *Context) *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Reload nginx, after testing the configuration",
		Long: `Asks the running nginx to re-read its configuration.

` + "`nginx -t`" + ` runs FIRST, always, and a failure stops the reload with exit 3. That
is the difference between being told the configuration is wrong and having a
worker process exit on start-up.

It runs even when ` + "`ngx apply`" + ` has just run one: the bytes on disk can change
between two commands, and the cost of asking again is one process.

Reloading is separate from applying on purpose. They are different decisions,
and a tool that coupled them could not express "stage this now, reload in the
maintenance window".`,
		Example: `  # apply and reload as two decisions
  ngx apply plan.json
  ngx reload

  # did it reload?
  ngx --field data.reloaded reload`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			execCtx, cancel := ctx.executionContext(cmd.Context())
			defer cancel()

			res, err := ctx.NewRuntime().Reload(execCtx)
			if err != nil {
				return err
			}

			env := ctx.NewEnvelope("reload")
			env.Data = ReloadData{
				Reloaded: res.Reloaded,
				Tested:   res.Tested.OK,
				Raw:      res.Raw,
			}
			for _, d := range res.Tested.Diagnostics {
				env.AddDiagnostic(d)
			}
			if !res.Reloaded {
				env.OK = false
			}
			if renderErr := ctx.Renderer.Render(env); renderErr != nil {
				return renderErr
			}

			switch {
			case !res.Tested.OK:
				return withoutRerender(output.InvalidConfig(
					"nginx rejected the configuration, so it was not reloaded"))
			case !res.Reloaded:
				return withoutRerender(output.Internal(nil,
					"the configuration is valid but the reload failed: %s", res.Raw))
			}
			return nil
		},
	}
}
