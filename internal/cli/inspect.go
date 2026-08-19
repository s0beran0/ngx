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

// InspectData is what inspect answers with: always the summary, and the tree
// only when it was asked for.
//
// Config is ABSENT, not empty, when no tree was requested -- an unavailable
// field is omitted, and "config":[] would claim the configuration has no
// files. The tree is not the default because on a real production nginx it is
// 1.6 MB of JSON, which is a context budget spent to answer a question about
// one file; --full-tree asks for it and the name says what it costs. Whenever
// Config IS present it holds at least one file: config.Parse always yields the
// top-level file, and a filter that matches nothing fails before reaching
// here.
type InspectData struct {
	Config  []*config.File `json:"config,omitempty"`
	Summary Summary        `json:"summary"`

	// Scope is present only when the tree is a subset. Its absence means
	// "not filtered".
	Scope *Scope `json:"scope,omitempty"`
}

// Scope marks the result as a deliberate subset, INSIDE data, where an agent
// reading only data trips over it while looking at config.
//
// ConfigHashOmitted is the other half of the same fact. config.Hash is
// computed over the tree it is handed, so a hash of a filtered tree is a valid
// hash OF A SUBSET and indistinguishable from the hash of the whole -- and the
// moment v0.2 uses the hash for optimistic locking, an agent could apply a
// change against a configuration the hash never covered. So a filtered answer
// drops meta.config_hash entirely, and says here that it did, rather than
// leaving the caller to guess why a field it relies on went missing.
type Scope struct {
	Partial           bool         `json:"partial"`
	Filters           ScopeFilters `json:"filters"`
	FilesEmitted      int          `json:"files_emitted"`
	ConfigHashOmitted bool         `json:"config_hash_omitted"`
}

// ScopeFilters echoes the filters that produced the subset, so the caller can
// tell a narrow answer from a small configuration without keeping the command
// line around.
type ScopeFilters struct {
	File   string `json:"file,omitempty"`
	Server string `json:"server,omitempty"`
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

	// A nil Config stays nil: the tree was not asked for, and an empty slice
	// here would put "config":[] back into the output.
	var files []*config.File
	if d.Config != nil {
		files = make([]*config.File, 0, len(d.Config))
		for _, f := range d.Config {
			files = append(files, &config.File{
				Path:   f.Path,
				Source: f.Source,
				Nodes:  redactNodes(f.Nodes, rs),
			})
		}
	}
	return InspectData{Config: files, Summary: d.Summary, Scope: d.Scope}
}

func redactNodes(nodes []*config.Node, rs output.RedactSet) []*config.Node {
	out := make([]*config.Node, 0, len(nodes))
	for _, n := range nodes {
		clone := *n
		// Argument by argument, never collapsing Args into a single
		// "***": the boundary between arguments is information the tree
		// exists to preserve, and the indices in RedactedArgs only mean
		// anything if they still address the original positions.
		if indices := rs.RedactedArgs(n.Directive, n.Args); len(indices) > 0 {
			args := make([]string, len(n.Args))
			copy(args, n.Args)
			for _, i := range indices {
				args[i] = output.RedactedValue
			}
			clone.Args = args
			clone.RedactedArgs = indices
		}
		if len(n.Block) > 0 {
			clone.Block = redactNodes(n.Block, rs)
		}
		out = append(out, &clone)
	}
	return out
}

func newInspectCmd(ctx *Context) *cobra.Command {
	var (
		combine  bool
		fullTree bool
		filter   inspectFilter
	)

	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Summary of the configuration; the tree with --file, --server or --full-tree",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := configPathOf(ctx)
			if path == "" {
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
			ctxExec, cancel := ctx.executionContext(cmd.Context())
			defer cancel()

			tr := ctx.ReadTransport(ctxExec)
			tree, err := config.Parse(config.ParseOptions{
				Path: path,
				Open: tr.Open,
				Glob: tr.Glob,
			})
			readDiags := ReadDiagnostics(tr)
			if err != nil {
				return parseFailure(withSudoHint(err, ctx), readDiags...)
			}

			if combine {
				tree, err = config.Combine(tree)
				if err != nil {
					return output.Internal(err, "%s", err.Error())
				}
			}

			env := ctx.NewEnvelope("inspect")
			env.Diagnostics = append(env.Diagnostics, readDiags...)

			// The summary always describes the WHOLE configuration that was
			// read, filtered or not. That is what makes it comparable
			// between calls -- "12 servers, and here is the one you asked
			// for" -- and scope.files_emitted is what says how much of it
			// came out.
			data := InspectData{Summary: summarize(tree)}

			switch {
			case filter.active():
				files, ferr := filter.apply(tree)
				if ferr != nil {
					// The read diagnostics survive the failure: which files
					// needed privilege is context the caller loses nowhere
					// else.
					ferr.Extras = append(ferr.Extras, readDiags...)
					return ferr
				}
				data.Config = files
				data.Scope = &Scope{
					Partial:           true,
					Filters:           ScopeFilters{File: filter.File, Server: filter.Server},
					FilesEmitted:      len(files),
					ConfigHashOmitted: true,
				}
				// meta.config_hash is deliberately NOT set: see Scope.
				env.AddDiagnostic(output.Diagnostic{
					Severity: output.SeverityInfo,
					Code:     CodePartialResult,
					Message: "partial result: data.config is the subset the filters name, " +
						"and meta.config_hash is omitted because it would be a valid hash of a subset",
				})
			default:
				if fullTree {
					data.Config = tree.Files
				}
				env.Meta.ConfigHash = tree.Hash
			}

			env.Data = data
			return ctx.Renderer.Render(env)
		},
	}

	cmd.Flags().BoolVar(&combine, "combine", false, "resolve the includes into a single tree")
	cmd.Flags().BoolVar(&fullTree, "full-tree", false,
		"emit every node of every file; on a production nginx this is megabytes of JSON")
	// The help text says "emitted", not "read": --file could prune the read
	// and does not yet, and --server structurally never can, because knowing
	// which file declares a server_name means reading the file. Promising a
	// saving here is the kind of claim a user discovers with a stopwatch.
	cmd.Flags().StringVar(&filter.File, "file", "",
		"emit only this file: a fragment matches anywhere in the path, a path starting with / matches exactly")
	cmd.Flags().StringVar(&filter.Server, "server", "",
		"emit only the server blocks with this server_name; combines with --file as AND")
	return cmd
}

// parseFailure translates the config.Parse failure into the correct exit code.
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
// withSudoHint adds, when the refusal was for permission and --sudo was not
// asked for, the sentence that turns a dead end into a next step.
//
// Without it the operator gets "no permission" and is left not knowing that
// the tool solves that -- and the wrong way out, loosening permissions on the
// server, is the most obvious one for whoever is in a hurry. DR5 prevents
// escalating on its own; nothing prevents saying how.
func withSudoHint(err error, ctx *Context) error {
	if ctx.Flags != nil && ctx.Flags.Sudo {
		return err
	}
	var problems config.ParseErrors
	if !errors.As(err, &problems) {
		return err
	}
	for i := range problems {
		// Branch on the CLASS, never on the message text. This used to match
		// the word "permission" in the message, and translating the project to
		// English removed the hint silently -- no test noticed, because no
		// test covered the branch. Class survives rewording and translation.
		if problems[i].Class != config.RefusalPermissionDenied {
			continue
		}
		problems[i].Message += ". Run with --sudo so that ngx reads with privilege " +
			"only the refused files; there is no need to change permissions on the target"
	}
	return problems
}

func parseFailure(err error, extras ...output.Diagnostic) error {
	var problems config.ParseErrors
	if !errors.As(err, &problems) || len(problems) == 0 {
		return output.Internal(err, "%s", err.Error())
	}

	items := make([]string, len(problems))
	for i, p := range problems {
		// With no known line (a file that did not even open), the `:0` would
		// be an invented reference. An unavailable field is omitted.
		if p.Line > 0 {
			items[i] = fmt.Sprintf("%s:%d: %s", p.File, p.Line, p.Message)
		} else {
			items[i] = fmt.Sprintf("%s: %s", p.File, p.Message)
		}
	}

	e := output.InvalidConfig("%s", strings.Join(items, "; "))
	e.Diag.File = problems[0].File
	e.Diag.Line = problems[0].Line
	e.Extras = append(e.Extras, extras...)
	e.Err = err
	return e
}

func configPathOf(ctx *Context) string {
	if ctx.Flags.ConfigPath != "" {
		return ctx.Flags.ConfigPath
	}
	if ctx.Settings != nil {
		return ctx.Settings.Nginx.Config
	}
	return ""
}

// summarize counts the blocks of the tree. Only directives that open a block
// (via HasBlock) enter the count: the fixture has "server 10.0.0.1:8080;"
// inside an upstream, which is also called "server" but is a simple directive,
// not a block -- counting by name alone would inflate Servers.
func summarize(t *config.Tree) Summary {
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
