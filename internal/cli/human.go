package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/output"
)

// This file holds the terminal presentation of the commands that read the
// configuration. The rule it follows is the project's rule, applied to text
// instead of to JSON: a field that was not determined produces no sentence.
// Rendering "0" or "stopped" for something that was never measured is the one
// failure mode that makes a human summary worse than the raw JSON it replaces
// -- JSON at least omits the key.
//
// None of this touches the serialized envelope. Render only reaches
// RenderHuman when the resolved format is human (a terminal, --human, or
// output.format in the settings file), so the JSON contract is untouched by
// everything below.

// RenderHuman writes the configuration summary in a handful of lines, and the
// outline of the blocks when a filter narrowed the answer to a few files.
//
// --full-tree is the deliberate exception: it falls back to the indented JSON
// the renderer would have produced anyway. Whoever types the flag that says
// "megabytes of JSON" in its own help text asked for the tree, and summarizing
// it here would answer a question they did not ask. The two cases are told
// apart without a new field: a filtered answer carries Scope, --full-tree does
// not.
//
// The receiver is by value for the reason Redacted's is: Render type-asserts
// over what the command stored in env.Data, and inspect stores an InspectData,
// not a pointer to one.
func (d InspectData) RenderHuman(w io.Writer) error {
	if d.Config != nil && d.Scope == nil {
		return renderIndentedJSON(w, d)
	}

	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, summaryLine(d.Summary))
	if d.Scope != nil {
		fmt.Fprintln(bw, scopeLine(*d.Scope, d.Summary.Files))
	}
	for _, f := range d.Config {
		// "emitted", not "in this file": this method only ever runs on a
		// filtered answer, so the count describes the subset that came out.
		// Printing it as the file's own size would be a number the reader
		// could check against the file and find wrong.
		fmt.Fprintf(bw, "\n%s  (%s emitted)\n", f.Path, plural(countDirectives(f.Nodes), "directive", "directives"))
		writeOutline(bw, f.Nodes, 1)
	}
	// bufio latches the first write error and Flush returns it, so checking
	// here covers every Fprintf above.
	if err := bw.Flush(); err != nil {
		return output.Internal(err, "failed to write the output")
	}
	return nil
}

// RenderHuman lists the matches one per line, prefixed by where each one is,
// which is the form a person can act on: the file and the line are what they
// will open next.
//
// The count comes first because it is the answer to "did this match anything",
// and it is the only line printed when nothing matched -- the diagnostic that
// says WHY (which directives are in scope, which values exist) was already
// written by the renderer before this method was called.
func (d GetData) RenderHuman(w io.Writer) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "%s in %s\n",
		plural(len(d.Matches), "match", "matches"),
		plural(countFiles(d.Matches), "file", "files"))

	if len(d.Matches) > 0 {
		fmt.Fprintln(bw)
		// The location column is padded to the width of the longest one, so
		// the directives line up and the list can be read down instead of
		// across.
		tw := tabwriter.NewWriter(bw, 0, 0, 2, ' ', 0)
		for _, n := range d.Matches {
			fmt.Fprintf(tw, "%s:%d\t%s\n", n.File, n.Line, matchText(n))
		}
		if err := tw.Flush(); err != nil {
			return output.Internal(err, "failed to write the output")
		}
	}

	if err := bw.Flush(); err != nil {
		return output.Internal(err, "failed to write the output")
	}
	return nil
}

// VersionData is the payload of `ngx version`. It is a named map, and not a
// struct, on purpose: the JSON has to come out byte-identical to what a
// map[string]string produced before this type existed -- same keys, same
// alphabetical order, same omission of a key that was never set. The type
// exists only to hang RenderHuman off it.
type VersionData map[string]string

// RenderHuman prints one "key: value" per line, in the order a person asks
// for them. A key that is not in the map produces no line, which is the same
// omission rule the JSON follows.
func (d VersionData) RenderHuman(w io.Writer) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "ngx %s\n", d["version"])
	for _, key := range []string{"install_channel", "update_public_key"} {
		if value, ok := d[key]; ok {
			fmt.Fprintf(bw, "%s: %s\n", strings.ReplaceAll(key, "_", " "), value)
		}
	}
	if err := bw.Flush(); err != nil {
		return output.Internal(err, "failed to write the output")
	}
	return nil
}

// renderIndentedJSON reproduces exactly what output.Renderer does with data
// that does not know how to present itself. It is what --full-tree falls back
// to, and duplicating the two lines here is cheaper than exporting a hook
// from the renderer for a single caller.
func renderIndentedJSON(w io.Writer, data any) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return output.Internal(err, "failed to serialize the output")
	}
	if _, err := fmt.Fprintln(w, string(b)); err != nil {
		return output.Internal(err, "failed to write the output")
	}
	return nil
}

// summaryLine is the one line that says how big the configuration is. The
// four counters are always available -- they are computed over the tree that
// was read -- so this line is never conditional.
func summaryLine(s Summary) string {
	return fmt.Sprintf("%s, %s, %s, %s",
		plural(s.Files, "file", "files"),
		plural(s.Servers, "server", "servers"),
		plural(s.Locations, "location", "locations"),
		plural(s.Upstreams, "upstream", "upstreams"))
}

// scopeLine says that the answer is a subset and what defines it, which is
// the same fact data.scope carries for a program. Without it a human reading
// "1 file" beside a 132-file configuration would think the filter found
// everything there is.
func scopeLine(s Scope, filesRead int) string {
	var b strings.Builder
	b.WriteString("scope: ")
	if filters := filterPairs(s.Filters); len(filters) > 0 {
		b.WriteString(strings.Join(filters, " "))
	} else {
		b.WriteString("filtered")
	}
	fmt.Fprintf(&b, " (%d of %d files emitted", s.FilesEmitted, filesRead)
	if s.ConfigHashOmitted {
		b.WriteString("; config_hash omitted, it would describe a subset")
	}
	b.WriteString(")")
	return b.String()
}

// filterPairs echoes only the filters that were given. An unused filter
// prints nothing rather than an empty value: "server=" reads like a filter
// that matched nothing.
func filterPairs(f ScopeFilters) []string {
	pairs := make([]string, 0, 5)
	for _, p := range []struct{ name, value string }{
		{"file", f.File},
		{"server", f.Server},
		{"directive", f.Directive},
		{"in", f.In},
		{"value", f.Value},
	} {
		if p.value != "" {
			pairs = append(pairs, p.name+"="+p.value)
		}
	}
	return pairs
}

// writeOutline prints one line per block, indented by nesting depth and
// prefixed by the line it opens on.
//
// Only blocks are printed. A simple directive would turn the outline into the
// file itself -- which --format nginx already emits, cheaper and in a syntax
// the reader knows -- while the blocks are the structure a person is looking
// for when they ask "how is this file organized". The count printed beside the
// file path is what keeps the simple directives from vanishing silently.
func writeOutline(w *bufio.Writer, nodes []*config.Node, depth int) {
	for _, n := range nodes {
		if !n.HasBlock() {
			continue
		}
		fmt.Fprintf(w, "%6d  %s%s\n", n.Line, strings.Repeat("  ", depth), directiveText(n))
		writeOutline(w, n.Block, depth+1)
	}
}

// directiveText is the directive as it would be written, name and arguments.
// The arguments come from the tree, so a redacted one is already "***" here:
// this renders the copy the renderer produced, never the original values.
func directiveText(n *config.Node) string {
	if len(n.Args) == 0 {
		return n.Directive
	}
	return n.Directive + " " + strings.Join(n.Args, " ")
}

// matchText is directiveText plus, for a match that opens a block, how much
// is inside it. The block itself is not printed: a match on `server` carries
// the whole site, and unfolding it here would bury the list of matches the
// line is part of. Saying how many directives it holds is what keeps the
// omission visible instead of making the block look empty.
func matchText(n *config.Node) string {
	text := directiveText(n)
	if !n.HasBlock() {
		return text
	}
	return fmt.Sprintf("%s { %s }", text, plural(len(n.Block), "directive", "directives"))
}

// countDirectives counts the nodes of a file, at every depth, excluding
// comments -- which are nodes in this tree but are not directives, and
// counting them would inflate a number a reader compares against the file.
func countDirectives(nodes []*config.Node) int {
	total := 0
	for _, n := range nodes {
		if n.IsComment() {
			continue
		}
		total++
		total += countDirectives(n.Block)
	}
	return total
}

// plural writes the count with the right noun. It exists because "1 files" in
// the first line of every answer is the kind of detail that makes a tool look
// unfinished.
func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, pluralForm)
}
