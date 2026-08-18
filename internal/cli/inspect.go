package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/spf13/cobra"
)

// Summary is the one-line view of the configuration. It exists so the agent
// knows the size of what it is looking at without having to count nodes.
type Summary struct {
	Files     int `json:"files"`
	Servers   int `json:"servers"`
	Locations int `json:"locations"`
	Upstreams int `json:"upstreams"`
}

// InspectData is the complete dump: tree plus summary.
type InspectData struct {
	Config  []*config.File `json:"config"`
	Summary Summary        `json:"summary"`
}

// Redacted returns a copy with the sensitive values replaced. The copy is deep
// on the affected nodes: the original tree is never changed, otherwise a later
// fmt would write *** into the user's file.
//
// The receiver is by value, not by pointer: Render does "data.(Redactable)"
// over what is stored in env.Data, and RunE stores an InspectData by value
// (not *InspectData). A pointer receiver here would make that assertion fail
// silently -- Data would go out intact, with no error and no warning, even
// with redaction rules active (see the comment on the Redact field in
// output.Renderer).
func (d InspectData) Redacted(rs output.RedactSet) any {
	if rs.Empty() {
		return d
	}

	arquivos := make([]*config.File, 0, len(d.Config))
	for _, f := range d.Config {
		arquivos = append(arquivos, &config.File{
			Path:   f.Path,
			Source: f.Source,
			Nodes:  redigirNodes(f.Nodes, rs),
		})
	}
	return InspectData{Config: arquivos, Summary: d.Summary}
}

func redigirNodes(nodes []*config.Node, rs output.RedactSet) []*config.Node {
	saida := make([]*config.Node, 0, len(nodes))
	for _, n := range nodes {
		copia := *n
		if rs.Matches(n.Directive, n.Args) {
			copia.Args = []string{output.RedactedValue}
		}
		if len(n.Block) > 0 {
			copia.Block = redigirNodes(n.Block, rs)
		}
		saida = append(saida, &copia)
	}
	return saida
}

func newInspectCmd(ctx *Context) *cobra.Command {
	var combine bool

	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Complete dump: configuration tree and summary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			caminho := caminhoDaConfig(ctx)
			if caminho == "" {
				return output.Usage("provide the configuration with -c or in nginx.config")
			}

			// Open and Glob come from the transport, never from os/filepath
			// directly: pointed at a remote host, a local Glob would list the
			// files of the operator's machine and present them as the
			// server's configuration (DR4). On the local target the transport
			// is exactly os.Open and filepath.Glob, so nothing changes.
			//
			// With --sudo the transport retries with privilege ONLY the file
			// that the ordinary read refused for permission. That is the real
			// case of a production nginx: most files are readable by
			// everyone, and a handful hold credentials and stay restricted
			// to root.
			ctxExec, cancelar := ctx.contextoDeExecucao(cmd.Context())
			defer cancelar()

			tr := ctx.TransporteDeLeitura(ctxExec)
			tree, err := config.Parse(config.ParseOptions{
				Path: caminho,
				Open: tr.Open,
				Glob: tr.Glob,
			})
			diagsLeitura := DiagnosticosDeLeitura(tr)
			if err != nil {
				return erroDeParse(comDicaDeSudo(err, ctx), diagsLeitura...)
			}

			if combine {
				tree, err = config.Combine(tree)
				if err != nil {
					return output.Internal(err, "%s", err.Error())
				}
			}

			env := ctx.NovoEnvelope("inspect")
			env.Diagnostics = append(env.Diagnostics, diagsLeitura...)
			env.Data = InspectData{Config: tree.Files, Summary: resumir(tree)}
			env.Meta.ConfigHash = tree.Hash
			return ctx.Renderer.Render(env)
		},
	}

	cmd.Flags().BoolVar(&combine, "combine", false, "resolve the includes into a single tree")
	return cmd
}

// erroDeParse translates the config.Parse failure into the correct exit code.
//
// config.ParseErrors represents invalid user configuration -- a syntax error,
// an include pointing at a nonexistent file -- and is exit 3
// (output.InvalidConfig), not exit 1 (output.Internal): the one that got it
// wrong was the .conf, not ngx itself. Any other failure (missing file, IO
// error) stays exit 1, because there the -c flag was correct and it was the
// disk that did not match.
//
// Each item of ParseErrors carries its own File and Line. They are preserved
// in the Diagnostic (instead of becoming just text inside Message) so that the
// output points at the exact place of the problem; when there is more than one
// item, each appears located in the message, instead of a single generic line.
// comDicaDeSudo adds, when the refusal was for permission and --sudo was not
// asked for, the sentence that turns a dead end into a next step.
//
// Without it the operator gets "no permission" and is left not knowing that
// the tool solves that -- and the wrong way out, loosening permissions on the
// server, is the most obvious one for whoever is in a hurry. DR5 prevents
// escalating on its own; nothing prevents saying how.
func comDicaDeSudo(err error, ctx *Context) error {
	if ctx.Flags != nil && ctx.Flags.Sudo {
		return err
	}
	var problemas config.ParseErrors
	if !errors.As(err, &problemas) {
		return err
	}
	for i := range problemas {
		if problemas[i].Classe != config.RecusaFalhaDeLeitura ||
			!strings.Contains(problemas[i].Message, "permissao") {
			continue
		}
		problemas[i].Message += ". Run with --sudo so that ngx reads with privilege " +
			"only the refused files; there is no need to change permissions on the target"
	}
	return problemas
}

func erroDeParse(err error, extras ...output.Diagnostic) error {
	var problemas config.ParseErrors
	if !errors.As(err, &problemas) || len(problemas) == 0 {
		return output.Internal(err, "%s", err.Error())
	}

	itens := make([]string, len(problemas))
	for i, p := range problemas {
		// With no known line (a file that did not even open), the `:0` would
		// be an invented reference. An unavailable field is omitted.
		if p.Line > 0 {
			itens[i] = fmt.Sprintf("%s:%d: %s", p.File, p.Line, p.Message)
		} else {
			itens[i] = fmt.Sprintf("%s: %s", p.File, p.Message)
		}
	}

	e := output.InvalidConfig("%s", strings.Join(itens, "; "))
	e.Diag.File = problemas[0].File
	e.Diag.Line = problemas[0].Line
	e.Extras = append(e.Extras, extras...)
	e.Err = err
	return e
}

func caminhoDaConfig(ctx *Context) string {
	if ctx.Flags.ConfigPath != "" {
		return ctx.Flags.ConfigPath
	}
	if ctx.Settings != nil {
		return ctx.Settings.Nginx.Config
	}
	return ""
}

// resumir counts the blocks of the tree. Only directives that open a block
// (via HasBlock) enter the count: the fixture has "server 10.0.0.1:8080;"
// inside an upstream, which is also called "server" but is a simple directive,
// not a block -- counting by name alone would inflate Servers.
func resumir(t *config.Tree) Summary {
	s := Summary{Files: len(t.Files)}
	t.Walk(func(n *config.Node) bool {
		if !n.HasBlock() {
			return true
		}
		switch n.Directive {
		case "server":
			s.Servers++
		case "location":
			s.Locations++
		case "upstream":
			s.Upstreams++
		}
		return true
	})
	return s
}
