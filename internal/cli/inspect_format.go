package cli

import (
	"bufio"
	"fmt"
	"io"
	"strconv"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/output"
)

// RenderNginx writes the configuration back as nginx TEXT instead of as the
// tree. Measured on this project's own fixture, the same file is 351 bytes
// this way and 2,635 bytes as JSON -- and nginx syntax is the one every model
// already reads, so for "how is site X configured?" this is at once the
// cheapest and the most familiar answer.
//
// What is emitted is the byte range of each node that is IN THE ANSWER, in
// order, never the whole file: with --server the answer is a couple of server
// blocks, and printing the file around them would put back exactly what the
// filter was asked to take out. Comments are nodes too, so they survive; what
// does not survive is the blank space between two top-level nodes.
//
// The receiver is by value for the same reason Redacted's is: the renderer
// type-asserts over what is stored in env.Data, and the command stores an
// InspectData, not a pointer to one.
func (d InspectData) RenderNginx(w io.Writer) error {
	if d.Config == nil {
		return output.Usage("--format nginx: no tree was read; ask for one with --full-tree, --file or --server")
	}

	bw := bufio.NewWriter(w)
	for i, f := range d.Config {
		if i > 0 {
			if _, err := bw.WriteString("\n"); err != nil {
				return output.Internal(err, "failed to write the output")
			}
		}
		// The path is a comment, not a bare line: the output stays valid
		// nginx, and a caller reading several files still knows where each
		// block came from.
		if _, err := fmt.Fprintf(bw, "# %s\n", f.Path); err != nil {
			return output.Internal(err, "failed to write the output")
		}
		if err := writeFileText(bw, f); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return output.Internal(err, "failed to write the output")
	}
	return nil
}

// writeFileText emits the text of one file's nodes, with the redaction
// substitutions applied.
func writeFileText(w *bufio.Writer, f *config.File) error {
	if len(f.Nodes) == 0 {
		return nil
	}
	if len(f.Source) == 0 {
		// The combined tree is the case that gets here: Combine keeps no
		// Source, because its nodes come from several files and each span
		// only means anything against the file it was cut from. Slicing
		// them against anything else would print another file's bytes.
		return output.Usage(
			"--format nginx: the source text of %s is not available; a combined tree keeps none, so ask without --combine",
			f.Path)
	}

	for _, n := range f.Nodes {
		if err := writeNode(w, f, n); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return output.Internal(err, "failed to write the output")
		}
	}
	return nil
}

// writeNode emits one node. The normal path is a byte-for-byte cut of the
// source, which is the whole point of the format: the author's formatting,
// comments and quoting come out as they were written.
//
// The exception is a node whose block was PRUNED by a filter. --server keeps
// the ancestors of the matched block (the "http" around it) with their Block
// narrowed to what matched, but Span still covers the whole original block --
// so cutting it would print the sibling servers the filter was asked to leave
// out. That is not merely more output than was asked for: those siblings are
// not in the tree that was redacted, so their ssl_certificate_key would come
// out in the clear. The wrapper is rebuilt around the kept children instead.
func writeNode(w *bufio.Writer, f *config.File, n *config.Node) error {
	if !n.HasBlock() || blockIsWhole(f.Source, n) {
		reps, err := subtreeReplacements(nil, n, f.Path)
		if err != nil {
			return err
		}
		return writeSlice(w, f, n.Span.Start, n.Span.End, reps)
	}

	reps, err := nodeReplacements(nil, n, f.Path)
	if err != nil {
		return err
	}
	if err := writeSlice(w, f, n.Span.Start, n.HeadSpan.End, reps); err != nil {
		return err
	}
	if _, err := w.WriteString(" {\n"); err != nil {
		return output.Internal(err, "failed to write the output")
	}
	for _, child := range n.Block {
		// The indentation the author wrote sits before the span, which
		// starts at the directive name. Copying it back keeps the rebuilt
		// block looking like the file it came from.
		if _, err := w.Write(lineIndent(f.Source, child.Span.Start)); err != nil {
			return output.Internal(err, "failed to write the output")
		}
		if err := writeNode(w, f, child); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return output.Internal(err, "failed to write the output")
		}
	}
	if _, err := w.WriteString("}"); err != nil {
		return output.Internal(err, "failed to write the output")
	}
	return nil
}

func writeSlice(w *bufio.Writer, f *config.File, start, end int, reps []output.Replacement) error {
	text, err := output.ApplySubstitutions(f.Source, start, end, reps)
	if err != nil {
		return err
	}
	if _, err := w.Write(text); err != nil {
		return output.Internal(err, "failed to write the output")
	}
	return nil
}

// blockIsWhole reports whether the node's children still tile its block --
// that is, whether everything between the head and the closing brace is
// either a child that is in the answer or layout.
//
// It is how a pruned block is told from an intact one WITHOUT a second source
// of truth: a directive that was filtered out leaves its name and its
// arguments sitting in the gap between two kept children, and a name is made
// of characters that layout is not. Comments are nodes here (crossplane parses
// them), so a comment between two directives does not look like pruning.
func blockIsWhole(src []byte, n *config.Node) bool {
	if n.HeadSpan.End < 0 || n.Span.End > len(src) || n.HeadSpan.End > n.Span.End {
		return false
	}
	cursor := n.HeadSpan.End
	for _, child := range n.Block {
		if child.Span.Start < cursor || child.Span.End > n.Span.End {
			return false
		}
		if !isLayout(src[cursor:child.Span.Start]) {
			return false
		}
		cursor = child.Span.End
	}
	return isLayout(src[cursor:n.Span.End])
}

// isLayout reports whether the bytes are only what separates directives:
// whitespace and the block delimiters. Anything else is a directive that is
// not in the answer.
func isLayout(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n', '{', '}':
		default:
			return false
		}
	}
	return true
}

// lineIndent returns the whitespace that precedes the offset on its own line,
// or nothing when there is anything else in between.
func lineIndent(src []byte, offset int) []byte {
	if offset > len(src) {
		return nil
	}
	i := offset
	for i > 0 && (src[i-1] == ' ' || src[i-1] == '\t') {
		i--
	}
	if i > 0 && src[i-1] != '\n' {
		return nil
	}
	return src[i:offset]
}

// redactionReplacements turns the marks the redacted copy carries into byte
// substitutions over the source. It reads Node.RedactedArgs -- the indices
// filled in by InspectData.Redacted -- and NOT the RedactSet: the rules were
// already applied when the copy was made, and applying them a second time
// here would be a second implementation of redaction, free to disagree with
// the first one.
//
// Each argument is replaced over its WHOLE lexeme (ArgSpans[i], quotes
// included), which is what makes "***" a valid argument on its own without
// having to escape anything.
func subtreeReplacements(reps []output.Replacement, n *config.Node, path string) ([]output.Replacement, error) {
	reps, err := nodeReplacements(reps, n, path)
	if err != nil {
		return nil, err
	}
	for _, child := range n.Block {
		if reps, err = subtreeReplacements(reps, child, path); err != nil {
			return nil, err
		}
	}
	return reps, nil
}

// nodeReplacements covers the arguments of one node, without descending.
func nodeReplacements(reps []output.Replacement, n *config.Node, path string) ([]output.Replacement, error) {
	if len(n.RedactedArgs) == 0 {
		return reps, nil
	}
	// A nil ArgSpans means UNAVAILABLE, not "no arguments", and it happens
	// for exactly one directive: "if", whose Args crossplane rewrites, so
	// there is no lexeme matching Args[i] to cut. Taking any span from
	// nearby would be guessing where the value starts, and a guessed cut
	// prints half a secret. The fallback is the one range that is still
	// true -- HeadSpan, the name plus every argument -- replaced whole.
	//
	// The length check is part of the same rule: a correspondence that is
	// not 1-to-1 is not a correspondence, and indexing it would be the same
	// guess by another route.
	if n.ArgSpans == nil || len(n.ArgSpans) != len(n.Args) {
		if n.HeadSpan.End <= n.HeadSpan.Start {
			return nil, output.Internal(nil,
				"%s: %s at line %d has a value to redact and no usable span",
				path, n.Directive, n.Line)
		}
		return append(reps, output.Replacement{
			Start: n.HeadSpan.Start,
			End:   n.HeadSpan.End,
			// The whole head becomes "directive ***": over-redacting every
			// argument of the directive, which is the safe side, and still
			// naming the directive, so the reader sees that it is there.
			Text: n.Directive + " " + output.RedactedValue,
		}), nil
	}

	for _, i := range n.RedactedArgs {
		if i < 0 || i >= len(n.ArgSpans) {
			return nil, output.Internal(nil,
				"%s: %s at line %d marks argument %d, which has no span",
				path, n.Directive, n.Line, i)
		}
		reps = append(reps, output.Replacement{
			Start: n.ArgSpans[i].Start,
			End:   n.ArgSpans[i].End,
			Text:  output.RedactedValue,
		})
	}
	return reps, nil
}

// Table answers --format table. The summary is flat -- four counters, one row
// -- and comes out as TSV; the tree is not, and is REFUSED with the reason.
//
// Flattening it would be the failure this format exists to avoid: a row per
// directive loses which server each "listen" belonged to, and nothing in the
// output would say so. A wrong answer that looks like an answer is worse than
// a refusal that names the format to use instead.
func (d InspectData) Table() (output.Table, error) {
	if d.Config != nil {
		return output.Table{}, output.Usage(
			"--format table: the configuration tree is nested and a table cannot hold it without losing which block each directive is in. " +
				"Use --json for the tree, or drop --full-tree/--file/--server for the summary")
	}
	return output.Table{
		Header: []string{"files", "servers", "locations", "upstreams"},
		Rows: [][]string{{
			strconv.Itoa(d.Summary.Files),
			strconv.Itoa(d.Summary.Servers),
			strconv.Itoa(d.Summary.Locations),
			strconv.Itoa(d.Summary.Upstreams),
		}},
	}, nil
}
