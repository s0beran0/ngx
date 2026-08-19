package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
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

	// Field is the dot path of --field. When it is not empty, Render writes
	// a single value taken from the envelope -- raw, with no quotes and no
	// envelope around it -- instead of the envelope itself.
	//
	// Inside the renderer, Field takes precedence over Format and over
	// Quiet. The conflict between --field and the flags that choose the
	// presentation is decided at the flag layer, because Format also comes
	// from output.format in the configuration file, where it is an ambient
	// default and not an explicit request; whatever gets here with Field
	// filled in wants one value.
	//
	// A path that does not exist is a usage error with NOTHING written:
	// an empty line on stdout would be assigned by a shell doing
	// V=$(ngx --field x.y status) and the script would carry on believing
	// it worked.
	Field string

	// Query is the jq expression of --query. When it is not empty, Render
	// applies it to the envelope and writes one line per result instead of
	// the envelope itself.
	//
	// It exists because jq was NOT installed on the production host this
	// project was validated against, and a tool that operates a remote
	// server should not require installing a second one to read its
	// answer. Everything --field cannot do -- projecting over a list,
	// filtering, counting -- lands here.
	//
	// It lives beside Field, at the same layer and after redaction, for
	// the same reason: the expression must read the envelope that WOULD
	// have gone out, never the tree in memory.
	Query string
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
	if r.Quiet && r.Field == "" && r.Query == "" && env.OK && !hasWarningOrWorse(env.Diagnostics) {
		return nil
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

	// Selection comes after redaction: --field and --query read the
	// envelope that would have been printed, so neither is a way around the
	// protection.
	//
	// The two are mutually exclusive at the flag layer; the order here only
	// decides what happens to a Renderer assembled by hand with both filled
	// in, and --query is the more expressive of the two.
	if r.Query != "" {
		return r.renderQuery(&out)
	}
	if r.Field != "" {
		return r.renderField(&out)
	}

	format, err := r.resolveFormat()
	if err != nil {
		return err
	}

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

// renderField writes the single value addressed by Field. Nothing is written
// before the value is resolved: on the failure path stdout stays byte-empty,
// which is what tells a shell that there is no value instead of handing it an
// empty string.
func (r *Renderer) renderField(env *Envelope) error {
	value, err := selectField(env, r.Field)
	if err != nil {
		return err
	}
	text, err := fieldText(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(r.Out, text); err != nil {
		return Internal(err, "failed to write the output")
	}
	return nil
}

// selectField navigates the envelope by a dot path. The navigation happens
// over the JSON shape of the envelope, not over the Go structs by reflection,
// because the JSON is the contract: the path typed in --field is the same one
// read in the output of --json, json tags and omitempty included. A field
// omitted from the JSON does not exist for --field either, which is coherent
// with "an unavailable field is omitted, never estimated".
func selectField(env *Envelope, path string) (any, error) {
	doc, err := envelopeDocument(env)
	if err != nil {
		return nil, err
	}

	current := doc
	for _, segment := range strings.Split(path, ".") {
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[segment]
			if !ok {
				return nil, errNoSuchField(path)
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil, errNoSuchField(path)
			}
			current = node[index]
		default:
			// A scalar (or a null) has nothing underneath it.
			return nil, errNoSuchField(path)
		}
	}
	return current, nil
}

// envelopeDocument turns the envelope into the generic JSON document that
// --field navigates and --query runs against. Both work over the JSON shape,
// not over the Go structs by reflection, because the JSON is the contract:
// the path or expression that is typed is the same one read in the output of
// --json, json tags and omitempty included.
//
// UseNumber keeps the literal from the JSON: without it every number would go
// through float64 and a duration of 30000 ms would reach the shell as 3e+04.
// gojq understands json.Number natively (see its encoder and operators), so
// the same document serves both flags.
func envelopeDocument(env *Envelope) (any, error) {
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, Internal(err, "failed to serialize the output")
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, Internal(err, "failed to serialize the output")
	}
	return doc, nil
}

func errNoSuchField(path string) *Error {
	return Usage("--field: the envelope has no value at %q", path)
}

// fieldText turns the selected value into the text that goes on stdout. A
// string comes out raw -- no quotes, which is the whole point of the flag.
// Everything else comes out as compact JSON: there is no raw form for an
// object or for a list, and a null is a value that exists, so it comes out as
// "null" and never as an empty line.
func fieldText(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case json.Number:
		return v.String(), nil
	case bool:
		return strconv.FormatBool(v), nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", Internal(err, "failed to serialize the output")
		}
		return string(b), nil
	}
}
