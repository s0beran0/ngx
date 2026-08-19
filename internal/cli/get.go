package cli

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/spf13/cobra"
)

// Diagnostic codes of get, in the same 0100 range of configuration as the
// filters of inspect. All three are INFO severity and leave the exit code at
// 0: asking for a directive that is not there is a question with an empty
// answer, not a failure of the invocation. An agent that treated "no listen
// in this file" as an error would retry forever.
const (
	// CodeDirectiveNoMatch: no node carries the name of --directive. The
	// message lists the names that ARE in scope, because an empty result
	// and a misspelt name are otherwise the same output.
	CodeDirectiveNoMatch = "NGX-0106"

	// CodeInNoMatch: the directive exists, but none of its occurrences sits
	// inside the block --in names. The message lists the blocks that DO
	// enclose it.
	CodeInNoMatch = "NGX-0107"

	// CodeValueNoMatch: the directive exists in scope, but no argument
	// equals --value. The message lists the values that were found.
	CodeValueNoMatch = "NGX-0108"
)

// GetData is what get answers with: the flat list of matching nodes.
//
// Matches is never nil -- an empty answer serializes as [] and a consumer
// doing `.data.matches | length` gets 0 instead of breaking. Each element is
// the SAME node the tree of inspect holds, whole (block included, when it has
// one), which is the property the oracle test pins down: a node out of get is
// byte-identical to that node inside `inspect --full-tree`. Anything else and
// get would be a second parser, free to drift into a second truth.
type GetData struct {
	Matches []*config.Node `json:"matches"`
	Summary Summary        `json:"summary"`

	// Scope is always present: every result of get is a subset by
	// definition, so partial is the only value it could take.
	Scope *Scope `json:"scope,omitempty"`

	// sources maps a file path to its original bytes, for --format nginx.
	// It is unexported because it is not part of the answer: it is the
	// material the nginx rendering cuts from. Node.File says which entry
	// each match belongs to.
	sources map[string][]byte
}

// Redacted mirrors InspectData.Redacted, and shares redactNodes with it on
// purpose: two implementations of redaction are two chances to disagree, and
// the oracle test compares the REDACTED output of get against the REDACTED
// tree of inspect, so a divergence here fails the build.
//
// The receiver is by value for the reason recorded on InspectData.Redacted:
// Render type-asserts over what the command stored in env.Data, and the
// command stores a GetData, not a pointer to one.
func (d GetData) Redacted(rs output.RedactSet) any {
	if rs.Empty() {
		return d
	}
	return GetData{
		Matches: redactNodes(d.Matches, rs),
		Summary: d.Summary,
		Scope:   d.Scope,
		sources: d.sources,
	}
}

// Table answers --format table, which is what a flat result was waiting for:
// "every listen in this configuration" is a list of rows, and as TSV it costs
// a fraction of the same list as JSON.
//
// Two cases are REFUSED rather than flattened, both for the reason
// InspectData.Table already records -- a wrong answer that looks like an
// answer is worse than a refusal that names the format to use instead:
//
//   - a match that opens a block, because its children have nowhere to go in
//     a row and nothing in the output would say they were dropped;
//   - an argument holding a space, because the args column joins the
//     arguments with one, and a consumer splitting it back would read one
//     directive's two arguments as three. This is the same failure the TSV
//     escaping rule exists to prevent, one level up: the rule covers tab,
//     newline and backslash, and the space is only ambiguous here because
//     this column packs a list into one field.
func (d GetData) Table() (output.Table, error) {
	rows := make([][]string, 0, len(d.Matches))
	for _, n := range d.Matches {
		if n.HasBlock() {
			return output.Table{}, output.Usage(
				"--format table: %s at %s:%d opens a block, and a row cannot hold what is inside it. "+
					"Use --json for the tree, or --format nginx for the configuration text",
				n.Directive, n.File, n.Line)
		}
		for _, a := range n.Args {
			if strings.ContainsAny(a, " \t\n") {
				return output.Table{}, output.Usage(
					"--format table: argument %q of %s at %s:%d holds a space, and the args column joins the "+
						"arguments with one, so the boundary between them would be lost. Use --json",
					a, n.Directive, n.File, n.Line)
			}
		}
		rows = append(rows, []string{
			n.ID,
			n.File,
			strconv.Itoa(n.Line),
			n.Directive,
			strings.Join(n.Args, " "),
		})
	}
	return output.Table{
		Header: []string{"id", "file", "line", "directive", "args"},
		Rows:   rows,
	}, nil
}

// RenderNginx emits the source text of the matched nodes instead of the tree.
// It groups the matches by the file they came from and hands the groups to
// the rendering inspect already has: the two commands print the same bytes
// for the same node because it is the same code doing the cutting.
func (d GetData) RenderNginx(w io.Writer) error {
	groups := make([]*config.File, 0, 1)
	index := map[string]*config.File{}
	for _, n := range d.Matches {
		f, ok := index[n.File]
		if !ok {
			f = &config.File{Path: n.File, Source: d.sources[n.File]}
			index[n.File] = f
			groups = append(groups, f)
		}
		f.Nodes = append(f.Nodes, n)
	}
	// Config is the non-nil empty slice when nothing matched: an empty answer
	// prints no configuration text, which is right, and is not the "no tree
	// was read" refusal, which would be wrong.
	return InspectData{Config: groups}.RenderNginx(w)
}

func newGetCmd(ctx *Context) *cobra.Command {
	var (
		filter getFilter
		scope  inspectFilter
	)

	cmd := &cobra.Command{
		Use:   "get",
		Short: "Every occurrence of a directive, optionally narrowed by block or by value",
		// The examples are the documentation an agent reads before its
		// first call (A4): flat flags, no selector language to get subtly
		// wrong, and one line per question it is likely to have.
		Example: `  ngx get --directive listen
  ngx get --directive server_name --value api.example.com
  ngx get --directive listen --in server
  ngx get --directive listen --format table
  ngx get --directive server --in http --format nginx`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := configPathOf(ctx)
			if path == "" {
				return output.Usage("provide the configuration with -c or in nginx.config")
			}

			ctxExec, cancel := ctx.executionContext(cmd.Context())
			defer cancel()

			// Open and Glob come from the transport for the reason inspect
			// records: pointed at a remote host, a local Glob would list the
			// operator's own machine and present it as the server's
			// configuration.
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

			env := ctx.NewEnvelope("get")
			env.Diagnostics = append(env.Diagnostics, readDiags...)

			files := tree.Files
			if scope.active() {
				// --file and --server keep the meaning they have on
				// inspect, ambiguity and no-match included: they name a
				// place, and naming no place at all is a usage error there
				// and here.
				narrowed, ferr := scope.apply(tree)
				if ferr != nil {
					ferr.Extras = append(ferr.Extras, readDiags...)
					return ferr
				}
				files = narrowed
			}

			// The ancestors of an included file come from where the include
			// is written, not from the file itself: on the layout every
			// distribution ships, the "http" that contains a server block
			// lives in nginx.conf and the block lives in sites-enabled.
			// Without this, --in http would answer "no match" on the most
			// ordinary nginx there is.
			matches, miss := filter.apply(files, config.IncludeAncestors(tree))
			if miss != nil {
				env.AddDiagnostic(*miss)
			}

			data := GetData{
				Matches: matches,
				Summary: summarize(tree),
				Scope: &Scope{
					Partial: true,
					Filters: ScopeFilters{
						File:      scope.File,
						Server:    scope.Server,
						Directive: filter.Directive,
						In:        filter.In,
						Value:     filter.Value,
					},
					FilesEmitted:      countFiles(matches),
					ConfigHashOmitted: true,
				},
				sources: sourceIndex(tree),
			}

			// meta.config_hash is deliberately NOT set. config.Hash is
			// computed over the tree it is handed, so a hash published beside
			// a subset is a valid hash OF A SUBSET, indistinguishable from
			// the hash of the whole -- and the moment v0.2 uses it for
			// optimistic locking, an agent could apply a change against a
			// configuration the hash never covered. Every result of get is a
			// subset, so get never publishes one.
			env.AddDiagnostic(output.Diagnostic{
				Severity: output.SeverityInfo,
				Code:     CodePartialResult,
				Message: "partial result: data.matches holds only the nodes the flags name, " +
					"and meta.config_hash is omitted because it would be a valid hash of a subset",
			})

			env.Data = data
			return ctx.Renderer.Render(env)
		},
	}

	// "occurrence", not "read": get has to read the whole configuration to
	// know which files hold the directive, and only --file could prune the
	// read -- which it does not do yet. Promising a saving here is the kind
	// of claim a user discovers with a stopwatch.
	cmd.Flags().StringVar(&filter.Directive, "directive", "",
		"emit every occurrence of this directive, by exact name (required)")
	cmd.Flags().StringVar(&filter.In, "in", "",
		"keep only the occurrences enclosed by this block, at any depth and across includes (e.g. server, http)")
	cmd.Flags().StringVar(&filter.Value, "value", "",
		"keep only the occurrences with an argument exactly equal to this value")
	cmd.Flags().StringVar(&scope.File, "file", "",
		"search only this file: a fragment matches anywhere in the path, a path starting with / matches exactly")
	cmd.Flags().StringVar(&scope.Server, "server", "",
		"search only the server blocks with this server_name; combines with --file as AND")
	// Required, because there is no default question. Without it get would
	// have to mean "everything", which is what inspect --full-tree already
	// is, under a name that promises the opposite.
	_ = cmd.MarkFlagRequired("directive")
	return cmd
}

// getFilter holds the three flat flags. There is no expression to parse and
// no path to compose: --directive listen cannot be subtly wrong, because
// there is nothing in it to be wrong about, while http.server.listen can be
// -- it can name a nesting that this configuration writes differently and
// come back empty with no way to tell that from "there is no listen".
//
// The three combine with AND, and each one narrows what the previous left:
// --directive listen --in server --value 443 is "the listen directives, of
// those the ones inside a server, of those the ones with an argument 443".
// That order is what lets the empty answer say WHICH of them emptied it.
type getFilter struct {
	Directive string
	In        string
	Value     string
}

// namedNode is a match together with the block names that enclose it,
// outermost first. The ancestors are collected during the walk because they
// cannot be recovered afterwards: Node has no parent pointer, and adding one
// would make the tree cyclic and unserializable.
type namedNode struct {
	node      *config.Node
	ancestors []string
}

// apply runs the three filters in order and returns the matches together with
// the diagnostic of the stage that emptied the result, if any.
//
// An empty result is NEVER an error: it is the answer to a question that has
// none, exit 0 and matches: []. What it must not be is silent, because an
// empty list and a misspelt name look identical -- so the diagnostic names
// what WAS available at the stage that emptied it, the same way --file
// already lists the files it read.
func (f getFilter) apply(files []*config.File, enclosing map[string][]string) ([]*config.Node, *output.Diagnostic) {
	empty := []*config.Node{}

	named := nodesNamed(files, f.Directive, enclosing)
	if len(named) == 0 {
		return empty, infoDiagnostic(CodeDirectiveNoMatch,
			"--directive %q matches no directive. In scope: %s",
			f.Directive, formatCandidates(distinctDirectives(files)))
	}

	inScope := named
	if f.In != "" {
		inScope = keepEnclosedBy(named, f.In)
		if len(inScope) == 0 {
			return empty, infoDiagnostic(CodeInNoMatch,
				"--in %q matches no block: %d occurrence(s) of %q were found, enclosed by: %s",
				f.In, len(named), f.Directive, formatCandidates(distinctAncestors(named)))
		}
	}

	if f.Value == "" {
		return nodesOf(inScope), nil
	}
	matches := make([]*config.Node, 0, 1)
	for _, m := range inScope {
		// Equality against ONE argument, never a substring over the joined
		// arguments: "listen 443 ssl" has an argument 443 and an argument
		// ssl, and a substring rule would also match 44 and make --value 80
		// hit "listen 8080". Being strict is what makes the answer
		// trustworthy; the diagnostic below is what keeps it usable, by
		// listing the values that are actually there.
		if slices.Contains(m.node.Args, f.Value) {
			matches = append(matches, m.node)
		}
	}
	if len(matches) == 0 {
		return empty, infoDiagnostic(CodeValueNoMatch,
			"--value %q matches no argument of %q. Values found: %s",
			f.Value, f.Directive, formatCandidates(distinctArgs(inScope)))
	}
	return matches, nil
}

// nodesNamed walks the files and collects the nodes carrying the name,
// keeping the block names that enclose each one.
//
// A match comes out WHOLE, block included when it has one: asking for a
// server means asking for what is inside it, and the sub-tree that comes back
// is the same object the full tree holds. The walk still descends into a
// match, because nginx nests directives of the same name -- a server inside
// http inside a stream, a location inside a location.
func nodesNamed(files []*config.File, name string, enclosing map[string][]string) []namedNode {
	out := make([]namedNode, 0, 4)
	var walk func(nodes []*config.Node, ancestors []string)
	walk = func(nodes []*config.Node, ancestors []string) {
		for _, n := range nodes {
			// A comment is a node with Directive "#", and it is never a
			// directive somebody asked for.
			if !n.IsComment() && n.Directive == name {
				out = append(out, namedNode{node: n, ancestors: slices.Clone(ancestors)})
			}
			if n.HasBlock() {
				// The child slice is built fresh instead of appended in
				// place: siblings would otherwise share a backing array and
				// one of them would overwrite the ancestor the next one is
				// about to record.
				walk(n.Block, append(slices.Clone(ancestors), n.Directive))
			}
		}
	}
	for _, f := range files {
		walk(f.Nodes, enclosing[f.Path])
	}
	return out
}

// keepEnclosedBy keeps the matches that have block among their ancestors, at
// any depth.
//
// Any depth, and not just the immediate parent, because that is what "in"
// means when read by somebody who has not read a specification: a listen
// inside a location inside a server IS inside a server, and answering "no
// match" for it would be technically defensible and practically a lie. --in
// http, which is only ever an ancestor and never a parent, would otherwise
// match nothing at all.
func keepEnclosedBy(named []namedNode, block string) []namedNode {
	out := make([]namedNode, 0, len(named))
	for _, m := range named {
		if slices.Contains(m.ancestors, block) {
			out = append(out, m)
		}
	}
	return out
}

func nodesOf(named []namedNode) []*config.Node {
	out := make([]*config.Node, 0, len(named))
	for _, m := range named {
		out = append(out, m.node)
	}
	return out
}

// distinctDirectives lists the directive names in scope, in tree order and
// without repeats. It is what turns "no match" into a next step.
func distinctDirectives(files []*config.File) []string {
	out := make([]string, 0, 16)
	seen := map[string]bool{}
	for _, f := range files {
		walkNodes(f.Nodes, func(n *config.Node) {
			if n.IsComment() || seen[n.Directive] {
				return
			}
			seen[n.Directive] = true
			out = append(out, n.Directive)
		})
	}
	return out
}

// distinctAncestors lists the blocks that enclose the found occurrences, in
// tree order and without repeats.
func distinctAncestors(named []namedNode) []string {
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, m := range named {
		for _, a := range m.ancestors {
			if seen[a] {
				continue
			}
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// distinctArgs lists the argument values the found occurrences carry, in tree
// order and without repeats.
func distinctArgs(named []namedNode) []string {
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, m := range named {
		for _, a := range m.node.Args {
			if a == "" || seen[a] {
				continue
			}
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// countFiles counts the distinct files the matches came from, which is what
// scope.files_emitted means for a result made of nodes.
func countFiles(matches []*config.Node) int {
	seen := map[string]bool{}
	for _, n := range matches {
		seen[n.File] = true
	}
	return len(seen)
}

// sourceIndex maps each read file to its original bytes, for --format nginx.
// A combined tree keeps no source, so the entry is empty and the rendering
// refuses with the reason instead of cutting another file's bytes.
func sourceIndex(t *config.Tree) map[string][]byte {
	out := make(map[string][]byte, len(t.Files))
	for _, f := range t.Files {
		out[f.Path] = f.Source
	}
	return out
}

// infoDiagnostic builds the "nothing matched" finding. Info severity, never
// error: AddDiagnostic brings the envelope's ok down for an error, and an
// empty answer is a true answer -- ok stays true and the exit code stays 0.
func infoDiagnostic(code, format string, args ...any) *output.Diagnostic {
	return &output.Diagnostic{
		Severity: output.SeverityInfo,
		Code:     code,
		Message:  fmt.Sprintf(format, args...),
	}
}
