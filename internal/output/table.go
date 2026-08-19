package output

import (
	"bufio"
	"errors"
	"strings"
)

// TableRenderable is implemented by data that is FLAT -- a list of rows with
// the same columns -- and can therefore be answered as TSV.
//
// It exists for the "list every port / upstream / match" question, where the
// tabular answer is measurably cheaper: on a real 269-match result, TSV was
// 47% fewer bytes and roughly 60% fewer tokens than the same result as JSON.
//
// Data that is NESTED must return an error here instead of flattening itself.
// A tree squashed into rows is a wrong answer that looks like an answer: the
// consumer cannot tell which "listen" belonged to which server, and nothing in
// the output says so.
type TableRenderable interface {
	Table() (Table, error)
}

// Table is a header and its rows. Both are plain strings: the escaping rule
// (see EscapeTSV) is applied at write time, so an implementation never has to
// know it -- and cannot forget it.
type Table struct {
	Header []string
	Rows   [][]string
}

// tabEscape is the escaping rule of --format table, and it is the answer to
// H4: a TSV stream has no escaping rule of its own, and an unescaped tab
// inside a field silently produces one extra column -- the consumer reads a
// shifted row with no error at all, which is the worst of the available
// failures.
//
// The rule chosen is the one PostgreSQL's COPY ... TEXT and mysql --batch
// already use, so it is neither new nor ours to define: the backslash escapes
// itself, and tab, newline and carriage return become \t, \n and \r. It is
// reversible, so a consumer can recover the exact argument, and it is
// byte-for-byte identical to the original for every field that holds none of
// those four characters -- which is every ordinary nginx argument.
//
// Quotes are NOT touched. TSV has no quoting, so a quote is just a byte;
// escaping it would corrupt an argument that legitimately contains one
// (add_header X "a b" keeps its quotes in the tree).
var tabEscape = strings.NewReplacer(
	`\`, `\\`,
	"\t", `\t`,
	"\n", `\n`,
	"\r", `\r`,
)

// EscapeTSV applies the escaping rule of --format table to one field. The
// backslash is escaped together with the rest, in a single pass, so that a
// field that already holds "\t" as two literal characters does not come out
// indistinguishable from a real tab.
func EscapeTSV(field string) string { return tabEscape.Replace(field) }

// renderTable writes the data as TSV.
func (r *Renderer) renderTable(env *Envelope) error {
	tr, ok := env.Data.(TableRenderable)
	if !ok {
		return Usage("--format table: the output of %q is not a flat result; use --json", env.Command)
	}
	table, err := tr.Table()
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			return err
		}
		return Internal(err, "failed to render the table")
	}

	if err := writeDiagnosticComments(r.Out, env.Diagnostics); err != nil {
		return err
	}

	w := bufio.NewWriter(r.Out)
	if err := writeTSVRow(w, table.Header); err != nil {
		return err
	}
	for i, row := range table.Rows {
		// A row with the wrong number of fields is refused, never padded
		// nor truncated: it is the same failure the escaping rule exists to
		// prevent -- a shifted line the consumer reads without an error.
		if len(row) != len(table.Header) {
			return Internal(nil, "row %d has %d fields and the header has %d",
				i, len(row), len(table.Header))
		}
		if err := writeTSVRow(w, row); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return Internal(err, "failed to write the output")
	}
	return nil
}

func writeTSVRow(w *bufio.Writer, fields []string) error {
	for i, f := range fields {
		if i > 0 {
			if err := w.WriteByte('\t'); err != nil {
				return Internal(err, "failed to write the output")
			}
		}
		if _, err := w.WriteString(EscapeTSV(f)); err != nil {
			return Internal(err, "failed to write the output")
		}
	}
	if err := w.WriteByte('\n'); err != nil {
		return Internal(err, "failed to write the output")
	}
	return nil
}
