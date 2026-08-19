package output

import (
	"errors"
	"fmt"
	"io"
	"sort"
)

// NginxRenderable is implemented by data that can emit the nginx source text
// it describes, instead of the tree that was parsed out of it.
//
// It exists because the two are not the same answer to the same question.
// Measured on this project's own fixture, the same file is 351 bytes as nginx
// text and 2,635 bytes as the JSON tree, and the nginx syntax is the one every
// model already reads: for "how is site X configured?" the text is both the
// cheaper and the more familiar answer. The tree stays the answer for "give me
// the structure to process", where argument boundaries have to survive.
//
// The implementation receives ALREADY REDACTED data -- see Renderer.Render,
// which swaps Data for the Redactable copy before dispatching -- so the text
// it emits must be built by substituting the bytes the copy marks in
// Node.RedactedArgs. Emitting the raw source instead would be a new flag going
// around the whole redactor.
type NginxRenderable interface {
	RenderNginx(w io.Writer) error
}

// Replacement overwrites the bytes [Start, End) of a source with Text. It is
// how redaction reaches the source text: the tree is never rewritten, the
// bytes of one argument are.
//
// The range is meant to be a whole argument lexeme (config.Node.ArgSpans),
// quotes included. That is what makes the substitution self-contained: "***"
// written over the whole lexeme is a valid unquoted argument, while writing
// over the inside of a pair of quotes would leave the delimiters standing and
// force the replacement to be escaped for that quote style.
type Replacement struct {
	Start int
	End   int
	Text  string
}

// ApplySubstitutions returns the bytes of src[start:end) with every
// replacement applied. Offsets are absolute against src, including the ones
// inside the replacements, because that is how the spans of the tree are
// recorded and converting them at the call site is one more place to be off
// by one.
//
// Every failure mode is an error and none is a best-effort recovery: a
// replacement out of range, overlapping its neighbour or with a reversed
// range means the spans and the source do not describe the same file, and the
// output of a wrong cut is a config text with a secret half-visible in it.
// Refusing is the only safe answer -- see the Nil-means-UNAVAILABLE rule of
// config.Node.ArgSpans, which this function is the consumer of.
func ApplySubstitutions(src []byte, start, end int, reps []Replacement) ([]byte, error) {
	if start < 0 || end < start || end > len(src) {
		return nil, Internal(nil, "invalid source range [%d,%d) over %d bytes", start, end, len(src))
	}

	ordered := make([]Replacement, len(reps))
	copy(ordered, reps)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })

	out := make([]byte, 0, end-start)
	cursor := start
	for _, r := range ordered {
		if r.Start < start || r.End > end || r.End < r.Start {
			return nil, Internal(nil,
				"replacement [%d,%d) falls outside the range [%d,%d)", r.Start, r.End, start, end)
		}
		if r.Start < cursor {
			return nil, Internal(nil,
				"replacement [%d,%d) overlaps the previous one", r.Start, r.End)
		}
		out = append(out, src[cursor:r.Start]...)
		out = append(out, r.Text...)
		cursor = r.End
	}
	out = append(out, src[cursor:end]...)
	return out, nil
}

// renderNginx writes the source text of the envelope's data.
//
// The order of the two refusals matters: data that is not nginx text at all
// gets told that first, because answering it with "cannot be redacted" would
// point at the wrong problem.
func (r *Renderer) renderNginx(env *Envelope, redacted bool) error {
	nr, ok := env.Data.(NginxRenderable)
	if !ok {
		return Usage("--format nginx: the output of %q is not nginx configuration text; use --json or --human", env.Command)
	}

	// The gate that keeps this flag from becoming a way around the redactor.
	// Redaction of the source text is done by substituting the bytes the
	// Redactable copy marks; data that produced no such copy while rules are
	// active would emit the source untouched, secrets included.
	if !r.NoRedact && !r.Redact.Empty() && !redacted {
		return Internal(nil,
			"--format nginx: the output of %q cannot be redacted, so its source text is not emitted", env.Command)
	}

	if err := writeDiagnosticComments(r.Out, env.Diagnostics); err != nil {
		return err
	}
	if err := nr.RenderNginx(r.Out); err != nil {
		// A typed refusal from the data (no tree was requested, no source
		// text available) travels intact, with its own exit code and
		// message; anything else is a defect of ours.
		var e *Error
		if errors.As(err, &e) {
			return err
		}
		return Internal(err, "failed to render the configuration text")
	}
	return nil
}

// writeDiagnosticComments emits the diagnostics as "#" lines. Neither nginx
// text nor TSV has a field for them, and dropping them would silently lose
// exactly the warnings that must not be silent -- "redaction is OFF", a host
// key accepted without verification. A "#" line is a comment in nginx and the
// conventional comment in a TSV stream, so the same rule serves both.
func writeDiagnosticComments(w io.Writer, diags []Diagnostic) error {
	for _, d := range diags {
		loc := ""
		if d.File != "" {
			loc = " " + d.File
			if d.Line > 0 {
				loc = fmt.Sprintf("%s:%d", loc, d.Line)
			}
		}
		if _, err := fmt.Fprintf(w, "# %s: %s%s\n", d.Severity, d.Message, loc); err != nil {
			return Internal(err, "failed to write the diagnostic")
		}
	}
	return nil
}
