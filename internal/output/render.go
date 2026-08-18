package output

import (
	"encoding/json"
	"fmt"
	"io"
)

// Format selects the renderer. FormatAuto decides by TTY.
type Format string

const (
	FormatAuto  Format = "auto"
	FormatJSON  Format = "json"
	FormatHuman Format = "human"
)

// HumanRenderable is implemented by data that knows how to present itself to
// a human. Data that does not implement it falls back to indented JSON, which
// is more useful than printing the raw Go struct.
type HumanRenderable interface {
	RenderHuman(w io.Writer) error
}

// Renderer serializes the envelope. It is the only layer that writes output.
//
// Redaction covers only the envelope's Data field. Diagnostics and Meta never
// go through redaction: Diagnostic.Message is free text and there is no way
// to know which part of it is sensitive, so the mitigation is one of policy,
// not of code -- it is the responsibility of whoever produces a Diagnostic to
// never embed a directive value in the message.
type Renderer struct {
	Out    io.Writer
	Format Format
	IsTTY  bool
	// Redact is the set of active rules. Redaction reaches only the
	// top-level Redactable in Data: if Data implements Redactable, Render
	// swaps Data for the result of Redacted before serializing. Redaction
	// is NOT recursive -- it does not descend into fields, slices or maps
	// inside Data on its own. Any type used in Data that may carry a
	// sensitive value (for example, a whole configuration tree) needs to
	// implement Redactable and, inside its own method, propagate redaction
	// to its children. Data that does not implement Redactable comes out
	// intact, with no error and no warning, even with Redact filled in.
	Redact   RedactSet
	NoRedact bool
	Quiet    bool
}

// Render writes the envelope in the resolved format. It does not mutate the
// Data of the envelope it receives: the redacted version (when there is one)
// is used only for writing, and the caller keeps seeing the original data
// after the call.
func (r *Renderer) Render(env *Envelope) error {
	if r.NoRedact && !r.IsTTY {
		return Usage("--no-redact is only accepted when the output is a terminal")
	}

	// --quiet suppresses success, not warnings. An ok=true envelope may
	// carry a security diagnostic -- a host key accepted without
	// verification, a key rejected with a silent fallback to password --
	// and swallowing it would turn the escape silent, which is exactly what
	// DR1 forbids. Whoever asks for --quiet wants less noise, not fewer
	// alerts.
	if r.Quiet && env.OK && !hasWarningOrWorse(env.Diagnostics) {
		return nil
	}

	format, err := r.resolveFormat()
	if err != nil {
		return err
	}

	data := env.Data
	if !r.NoRedact && !r.Redact.Empty() {
		if red, ok := data.(Redactable); ok {
			data = red.Redacted(r.Redact)
		}
	}

	// out is a local copy of the envelope with the (possibly redacted) Data
	// swapped in; the caller's env.Data stays untouched.
	out := *env
	out.Data = data

	switch format {
	case FormatHuman:
		return r.renderHuman(&out)
	default:
		return r.renderJSON(&out)
	}
}

// resolveFormat decides the effective format. FormatAuto (or the zero value)
// decides by IsTTY. Any value outside auto/json/human is a usage error:
// Format usually comes from output.format in the configuration file, which is
// free-form string, and an invalid value must not silently become JSON.
func (r *Renderer) resolveFormat() (Format, error) {
	switch r.Format {
	case FormatAuto, "":
		if r.IsTTY {
			return FormatHuman, nil
		}
		return FormatJSON, nil
	case FormatJSON, FormatHuman:
		return r.Format, nil
	default:
		return "", Usage("invalid output format: %q", string(r.Format))
	}
}

func (r *Renderer) renderJSON(env *Envelope) error {
	enc := json.NewEncoder(r.Out)
	if r.IsTTY {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(env); err != nil {
		return Internal(err, "failed to serialize the output")
	}
	return nil
}

func (r *Renderer) renderHuman(env *Envelope) error {
	for _, d := range env.Diagnostics {
		loc := ""
		if d.File != "" {
			// Line and Column are optional by design (omitempty):
			// without a valid line, appending ":0:0" would be a fake
			// coordinate.
			if d.Line > 0 {
				switch {
				case d.Line > 0 && d.Column > 0:
					loc = fmt.Sprintf(" %s:%d:%d", d.File, d.Line, d.Column)
				case d.Line > 0:
					loc = fmt.Sprintf(" %s:%d", d.File, d.Line)
				default:
					// No known position: the file alone.
					// `file:0:0` looks like a defect and points
					// nowhere.
					loc = fmt.Sprintf(" %s", d.File)
				}
			} else {
				loc = fmt.Sprintf(" %s", d.File)
			}
		}
		if _, err := fmt.Fprintf(r.Out, "%s: %s%s\n", d.Severity, d.Message, loc); err != nil {
			return Internal(err, "failed to write the diagnostic")
		}
	}

	if hr, ok := env.Data.(HumanRenderable); ok {
		if err := hr.RenderHuman(r.Out); err != nil {
			return Internal(err, "failed to render the human output")
		}
		return nil
	}

	if env.Data == nil {
		return nil
	}
	b, err := json.MarshalIndent(env.Data, "", "  ")
	if err != nil {
		return Internal(err, "failed to serialize the output")
	}
	if _, err := fmt.Fprintln(r.Out, string(b)); err != nil {
		return Internal(err, "failed to write the output")
	}
	return nil
}

// hasWarningOrWorse reports whether there is a diagnostic that --quiet must not
// suppress. Info severity is informational and falls into silence; warning
// and error are signal, and suppressed signal is nonexistent signal.
func hasWarningOrWorse(diags []Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == SeverityWarning || d.Severity == SeverityError {
			return true
		}
	}
	return false
}
