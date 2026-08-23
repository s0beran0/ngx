package cli

import (
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/ops"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/plan"
)

// The commands that change a configuration.
//
// Every one of them PRODUCES A PLAN and writes nothing. That is the shape of
// v0.2 and it is not ceremony: the plan is reviewable, it is anchored to the
// configuration it was built from, and `ngx apply` refuses it if anything moved
// in between. A command that edited in one step would have nothing to show
// before acting and nothing to check when acting.
//
//	ngx set --ref conf.d/site.conf#s0.d0 --value 8443 --value ssl > plan.json
//	ngx apply plan.json
//
// The plan goes to stdout as the envelope's data, so the pipeline above is the
// ordinary one: a JSON document produced by one command and consumed by
// another.

// PlanData is what a plan-producing command returns.
type PlanData struct {
	Plan *plan.Plan `json:"plan"`

	// Summary is the human sentence, and it is in the DATA rather than only in
	// the terminal rendering because an agent reading JSON needs the same
	// one-line answer a person gets.
	Summary string `json:"summary"`
}

// loadTree reads the configuration the mutation commands operate on, in exactly
// the way inspect and get do, so a ref produced by one is valid for the others.
func loadTree(ctx *Context, cmd *cobra.Command) (*config.Tree, string, error) {
	path := configPathOf(ctx)
	if path == "" {
		return nil, "", output.Usage("provide the configuration with -c or in nginx.config")
	}

	execCtx, cancel := ctx.executionContext(cmd.Context())
	defer cancel()

	tr := ctx.ReadTransport(execCtx)
	tree, err := config.Parse(config.ParseOptions{
		Path: path,
		Open: tr.Open,
		Glob: tr.Glob,
	})
	if err != nil {
		return nil, "", parseFailure(withSudoHint(err, ctx), ReadDiagnostics(tr)...)
	}
	return tree, path, nil
}

// emitPlan renders a plan as the envelope's data and translates a refusal from
// ops into an exit code.
func emitPlan(ctx *Context, command string, p *plan.Plan, err error) error {
	if err != nil {
		return planRefusal(err)
	}
	if verr := p.Validate(); verr != nil {
		// A plan that fails its own validation is a defect in the operation
		// that built it, caught before it can be written to a file and applied
		// later by somebody who trusts it.
		return output.Internal(verr, "the plan ngx produced is not valid: %s", verr.Error())
	}

	env := ctx.NewEnvelope(command)
	env.Data = PlanData{Plan: p, Summary: p.Describe()}
	return ctx.Renderer.Render(env)
}

// planRefusal maps an ops refusal to an exit code by its CLASS, never by its
// text.
func planRefusal(err error) error {
	code, ok := ops.CodeOf(err)
	if !ok {
		return err
	}
	switch code {
	case ops.CodeRefNotFound, ops.CodeInvalidArguments, ops.CodeNoChange,
		ops.CodeUnsupportedTarget, ops.CodeNotIncluded, ops.CodeFileExists:
		// All of these are the caller asking for something ngx will not do,
		// which is a usage problem rather than a broken configuration or an
		// internal failure.
		return output.Usage("%s", err.Error())
	default:
		return output.Internal(err, "%s", err.Error())
	}
}

func newSetCmd(ctx *Context) *cobra.Command {
	var ref string
	var values []string

	cmd := &cobra.Command{
		Use:   "set",
		Short: "Produce a plan that changes a directive's arguments",
		Long: `Builds a plan that replaces the arguments of one directive.

It writes NOTHING. The plan goes to stdout, and ` + "`ngx apply`" + ` is what acts on
it -- so the change can be read before it happens, and refused if the
configuration moved in between.

--ref names the directive, and it comes from the output of ` + "`ngx get`" + ` or
` + "`ngx inspect`" + `: it is "<file>#<id>", because an id on its own does not name a
node (the first directive of every file under conf.d is s0.d0).

Only the directive's name and arguments are replaced. The terminator, any block,
the comments and every other byte of the file are outside the substitution and
cannot be disturbed by it.`,
		Example: `  # change a port
  ngx get -c /etc/nginx/nginx.conf --directive listen --format table
  ngx set -c /etc/nginx/nginx.conf --ref /etc/nginx/conf.d/site.conf#s0.d0 --value 8443 --value ssl

  # review, then apply
  ngx set ... > plan.json
  ngx apply plan.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tree, root, err := loadTree(ctx, cmd)
			if err != nil {
				return err
			}
			p, opErr := ops.Set(tree, root, ref, values)
			return emitPlan(ctx, "set", p, opErr)
		},
	}

	cmd.Flags().StringVar(&ref, "ref", "", "the directive to change, as \"<file>#<id>\"")
	cmd.Flags().StringArrayVar(&values, "value", nil,
		"an argument of the new directive; repeat for each one")
	_ = cmd.MarkFlagRequired("ref")
	return cmd
}

func newAddCmd(ctx *Context) *cobra.Command {
	var parent, directive string
	var values []string

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Produce a plan that adds a directive inside a block",
		Long: `Builds a plan that inserts a directive as the last child of a block.

It writes NOTHING; ` + "`ngx apply`" + ` does.

The indentation is COPIED from the block's existing children and the line ending
from the file, so a file indented with tabs stays indented with tabs and a CRLF
file stays CRLF. Nothing is reformatted.

--parent names the block, and it is a ref like any other.`,
		Example: `  # turn off server_tokens in one server
  ngx add -c /etc/nginx/nginx.conf --parent /etc/nginx/conf.d/site.conf#s0 \
    --directive server_tokens --value off`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tree, root, err := loadTree(ctx, cmd)
			if err != nil {
				return err
			}
			p, opErr := ops.Add(tree, root, parent, directive, values)
			return emitPlan(ctx, "add", p, opErr)
		},
	}

	cmd.Flags().StringVar(&parent, "parent", "", "the block to add to, as \"<file>#<id>\"")
	cmd.Flags().StringVar(&directive, "directive", "", "the name of the directive to add")
	cmd.Flags().StringArrayVar(&values, "value", nil,
		"an argument of the new directive; repeat for each one")
	_ = cmd.MarkFlagRequired("parent")
	_ = cmd.MarkFlagRequired("directive")
	return cmd
}

func newRmCmd(ctx *Context) *cobra.Command {
	var ref, file string

	cmd := &cobra.Command{
		Use:   "rm",
		Short: "Produce a plan that removes a directive or a .conf file",
		Long: `Builds a plan that removes something. It writes NOTHING; ` + "`ngx apply`" + ` does.

--ref removes one directive, taking its whole line when it is alone on it, and
leaving the blank lines around it untouched.

--file removes a whole .conf. It refuses a path nginx does not load -- a file
merely on disk is not ngx's to delete -- and it says how many server blocks and
locations disappear with it, because "deleted 1 file" is not what an operator
needs to know.`,
		Example: `  # one directive
  ngx rm -c /etc/nginx/nginx.conf --ref /etc/nginx/conf.d/site.conf#s0.d2

  # a whole site
  ngx rm -c /etc/nginx/nginx.conf --file /etc/nginx/conf.d/old-site.conf`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch {
			case ref == "" && file == "":
				return output.Usage("rm needs --ref (a directive) or --file (a whole .conf)")
			case ref != "" && file != "":
				return output.Usage("rm takes --ref or --file, not both: they remove different things")
			}

			tree, root, err := loadTree(ctx, cmd)
			if err != nil {
				return err
			}

			if file != "" {
				p, opErr := ops.DeleteFile(tree, root, file)
				return emitPlan(ctx, "rm", p, opErr)
			}
			p, opErr := ops.Remove(tree, root, ref)
			return emitPlan(ctx, "rm", p, opErr)
		},
	}

	cmd.Flags().StringVar(&ref, "ref", "", "the directive to remove, as \"<file>#<id>\"")
	cmd.Flags().StringVar(&file, "file", "", "the .conf to remove, an absolute path")
	return cmd
}

func newCreateCmd(ctx *Context) *cobra.Command {
	var file, content, from, mode string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Produce a plan that creates a .conf file",
		Long: `Builds a plan that creates a configuration file. It writes NOTHING; ` + "`ngx apply`" + ` does.

The check that matters is not whether the path is writable, it is whether nginx
will LOAD it: a .conf in a directory no ` + "`include`" + ` reaches is invisible to the
server, and the author would believe their site was configured. That is refused.

The content comes from --content or from --from (a file, or "-" for stdin).`,
		Example: `  # a new site, from a file
  ngx create -c /etc/nginx/nginx.conf --file /etc/nginx/conf.d/new.conf --from ./new.conf

  # or from stdin
  cat new.conf | ngx create -c /etc/nginx/nginx.conf --file /etc/nginx/conf.d/new.conf --from -`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := contentFrom(cmd, content, from)
			if err != nil {
				return err
			}

			perm, err := strconv.ParseUint(mode, 8, 32)
			if err != nil {
				return output.Usage("--mode is octal, like 0644: %s", err.Error())
			}

			tree, root, err := loadTree(ctx, cmd)
			if err != nil {
				return err
			}
			p, opErr := ops.CreateFile(tree, root, file, body, os.FileMode(perm))
			return emitPlan(ctx, "create", p, opErr)
		},
	}

	cmd.Flags().StringVar(&file, "file", "", "the .conf to create, an absolute path")
	cmd.Flags().StringVar(&content, "content", "", "the content, inline")
	cmd.Flags().StringVar(&from, "from", "", "read the content from this file, or \"-\" for stdin")
	cmd.Flags().StringVar(&mode, "mode", "0644", "the file mode, octal")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

// contentFrom resolves --content and --from into the bytes to write.
//
// Exactly one of them, because "both were given and one won" is a silent choice
// about somebody's configuration.
func contentFrom(cmd *cobra.Command, content, from string) (string, error) {
	switch {
	case content == "" && from == "":
		return "", output.Usage("create needs --content or --from")
	case content != "" && from != "":
		return "", output.Usage("create takes --content or --from, not both")
	case content != "":
		return content, nil
	case from == "-":
		body, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", output.Usage("could not read the content from stdin: %s", err.Error())
		}
		return string(body), nil
	default:
		body, err := os.ReadFile(from)
		if err != nil {
			return "", output.Usage("could not read %s: %s", from, err.Error())
		}
		return string(body), nil
	}
}
