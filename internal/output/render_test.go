package output_test

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

type redactableData struct {
	Value string `json:"value"`
}

func (d redactableData) Redacted(rs output.RedactSet) any {
	if rs.Matches("ssl_certificate_key", []string{d.Value}) {
		return redactableData{Value: output.RedactedValue}
	}
	return d
}

// nonRedactableData deliberately does not implement Redactable: it exists to
// pin the fail-open behavior when Data does not know how to redact itself.
type nonRedactableData struct {
	Value string `json:"value"`
}

type humanData struct{}

func (humanData) RenderHuman(w io.Writer) error {
	_, err := io.WriteString(w, "human output\n")
	return err
}

// redactableHumanData implements both interfaces: Redactable and
// HumanRenderable. It exists to prove that redaction also reaches the
// FormatHuman path, not only JSON.
type redactableHumanData struct {
	Value string `json:"value"`
}

func (d redactableHumanData) Redacted(rs output.RedactSet) any {
	if rs.Matches("ssl_certificate_key", []string{d.Value}) {
		return redactableHumanData{Value: output.RedactedValue}
	}
	return d
}

func (d redactableHumanData) RenderHuman(w io.Writer) error {
	_, err := io.WriteString(w, d.Value+"\n")
	return err
}

// The auto format without a TTY has to become JSON: that is the agent reading
// a pipe.
func TestFormatAutoWithoutTTYProducesJSON(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatAuto, IsTTY: false}

	require.NoError(t, r.Render(output.New("status")))

	var env output.Envelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, "status", env.Command)
}

// With a TTY, when the data implements HumanRenderable, RenderHuman is used
// instead of serializing the struct as JSON.
func TestFormatAutoWithTTYUsesRenderHumanWhenAvailable(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatAuto, IsTTY: true}

	env := output.New("status")
	env.Data = humanData{}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "human output")
}

// With a TTY but without RenderHuman on the data, the human format falls back
// to indented JSON instead of printing the raw Go struct.
func TestFormatHumanWithoutHumanRenderableFallsBackToIndentedJSON(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatHuman, IsTTY: true}

	env := output.New("status")
	env.Data = nonRedactableData{Value: "abc"}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "\"value\": \"abc\"")
}

// Without Data, the human format writes nothing beyond the diagnostics (the
// Data == nil early return).
func TestFormatHumanWithNilDataWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatHuman, IsTTY: true}

	require.NoError(t, r.Render(output.New("status")))

	require.Empty(t, buf.String())
}

// The human format prints each diagnostic with its location.
func TestFormatHumanPrintsDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatHuman, IsTTY: true}

	env := output.New("lint")
	env.AddDiagnostic(output.Diagnostic{
		Severity: output.SeverityWarning,
		Message:  "long line",
		File:     "nginx.conf",
		Line:     12,
		Column:   3,
	})
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "warning: long line nginx.conf:12:3")
}

// A diagnostic with a file but no line must not print the fake coordinate
// ":0:0" -- Line and Column are optional by design.
func TestFormatHumanDiagnosticWithFileButNoLineDoesNotPrintZeroZero(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatHuman, IsTTY: true}

	env := output.New("lint")
	env.AddDiagnostic(output.Diagnostic{
		Severity: output.SeverityWarning,
		Message:  "file without a line",
		File:     "nginx.conf",
	})
	require.NoError(t, r.Render(env))

	require.NotContains(t, buf.String(), ":0:0")
	require.Contains(t, buf.String(), "nginx.conf")
}

// The gate that redaction exists to close: a human at the terminal may see
// the secret, an agent reading the pipe cannot even ask for it. The point of
// the gate is that the secret never reaches the output -- if the check were
// moved after the switch, the buffer would no longer be empty here.
func TestNoRedactIsRefusedWithoutTTY(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, NoRedact: true}

	err := r.Render(output.New("get"))

	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
	require.Empty(t, buf.String())
}

func TestNoRedactIsAcceptedWithTTY(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{
		Out: &buf, Format: output.FormatJSON, IsTTY: true,
		Redact: set, NoRedact: true,
	}

	env := output.New("get")
	env.Data = redactableData{Value: "/etc/ssl/priv.key"}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "/etc/ssl/priv.key")
}

// Without --no-redact, the data goes through redaction before being
// serialized.
func TestRenderAppliesRedactionToData(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, Redact: set}

	env := output.New("get")
	env.Data = redactableData{Value: "/etc/ssl/priv.key"}
	require.NoError(t, r.Render(env))

	require.NotContains(t, buf.String(), "/etc/ssl/priv.key")
	require.Contains(t, buf.String(), output.RedactedValue)
}

// Redaction also covers the FormatHuman path, not only JSON: both go through
// the same block of code in Render, and that is exactly why this test matters
// -- it guards against someone moving redaction inside renderJSON, a change
// that would pass the whole JSON suite and leak the secret in the human
// output.
func TestRenderHumanAppliesRedactionToData(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{Out: &buf, Format: output.FormatHuman, IsTTY: true, Redact: set}

	env := output.New("get")
	env.Data = redactableHumanData{Value: "/etc/ssl/priv.key"}
	require.NoError(t, r.Render(env))

	require.NotContains(t, buf.String(), "/etc/ssl/priv.key")
	require.Contains(t, buf.String(), output.RedactedValue)
}

// Pins the fail-open documented on the Redact field: Data that does not
// implement Redactable comes out intact, with no error and no signal at all,
// even with redaction rules active. It is the most likely failure mode when a
// real tree (e.g. Task 13) gets plugged into Data without implementing the
// interface.
func TestRenderDoesNotRedactDataThatDoesNotImplementRedactable(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, Redact: set}

	env := output.New("get")
	env.Data = nonRedactableData{Value: "/etc/ssl/priv.key"}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "/etc/ssl/priv.key")
}

// Render does not mutate the caller's envelope: the original Data stays
// intact after the call, even when redaction swaps the serialized value.
func TestRenderDoesNotMutateCallerData(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, Redact: set}

	env := output.New("get")
	original := redactableData{Value: "/etc/ssl/priv.key"}
	env.Data = original
	require.NoError(t, r.Render(env))

	require.Equal(t, original, env.Data)
}

// A Format outside auto/json/human is a usage error, it never silently falls
// back to JSON. That matters because Format usually comes from output.format
// in the YAML configuration file, which is free-form string.
func TestInvalidFormatIsRefused(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.Format("xml"), IsTTY: false}

	err := r.Render(output.New("status"))

	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
	require.Empty(t, buf.String())
}

// Quiet suppresses the success output but never the error one: an agent needs
// to know what went wrong.
func TestQuietSuppressesSuccessButNotError(t *testing.T) {
	var success bytes.Buffer
	r := &output.Renderer{Out: &success, Format: output.FormatJSON, Quiet: true}
	require.NoError(t, r.Render(output.New("status")))
	require.Empty(t, success.String())

	var failure bytes.Buffer
	r2 := &output.Renderer{Out: &failure, Format: output.FormatJSON, Quiet: true}
	env := output.New("test")
	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityError, Message: "failed"})
	require.NoError(t, r2.Render(env))
	require.Contains(t, failure.String(), "failed")
}

// --quiet suppresses success, never a warning. An ok=true envelope may carry
// a security diagnostic (host key accepted without verification, redaction
// turned off); swallowing it makes the escape silent, which is what DR1
// forbids. The pair of cases exists to prove the distinction: without a
// warning it stays mute, with one it speaks.
func TestQuietSuppressesSuccessButNeverWarning(t *testing.T) {
	cases := []struct {
		name    string
		diags   []output.Diagnostic
		emitted bool
	}{
		{"a clean success stays mute", nil, false},
		{"info stays mute as well", []output.Diagnostic{{Severity: output.SeverityInfo, Code: "NGX-0212"}}, false},
		{"a warning speaks", []output.Diagnostic{{Severity: output.SeverityWarning, Code: "NGX-0211"}}, true},
		{"an error speaks", []output.Diagnostic{{Severity: output.SeverityError, Code: "NGX-0201"}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			r := &output.Renderer{Out: &out, Quiet: true, Format: output.FormatJSON}
			env := &output.Envelope{OK: true, Command: "inspect", Diagnostics: c.diags}
			require.NoError(t, r.Render(env))
			if c.emitted {
				require.NotEmpty(t, out.String(), "a suppressed warning is a nonexistent warning")
			} else {
				require.Empty(t, out.String())
			}
		})
	}
}

// --field prints the scalar raw: no quotes, no envelope, exactly one newline.
// That is the whole point -- the value goes straight into a shell variable
// without a JSON parser in between.
func TestFieldPrintsScalarRaw(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Field: "data.version"}

	env := output.New("version")
	env.Data = map[string]string{"version": "1.20.1"}
	require.NoError(t, r.Render(env))

	require.Equal(t, "1.20.1\n", buf.String())
}

// A number comes out as it is written in the JSON, not through float64: a
// duration of 30000 ms must not reach the shell as 3e+04.
func TestFieldPrintsNumberWithoutScientificNotation(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Field: "meta.duration_ms"}

	env := output.New("status")
	env.Meta.DurationMS = 30000
	require.NoError(t, r.Render(env))

	require.Equal(t, "30000\n", buf.String())
}

// A boolean is a scalar as well: `ok` is the field an agent reads the most.
func TestFieldPrintsBooleanRaw(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Field: "ok"}

	require.NoError(t, r.Render(output.New("status")))

	require.Equal(t, "true\n", buf.String())
}

// There is no raw form for an object or for a list, so they come out as
// compact JSON -- one line, which is still pipeable.
func TestFieldPrintsObjectAndListAsCompactJSON(t *testing.T) {
	cases := []struct {
		name  string
		field string
		want  string
	}{
		{"object", "data", "{\"version\":\"1.20.1\"}\n"},
		{"empty list", "diagnostics", "[]\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Field: c.field}

			env := output.New("version")
			env.Data = map[string]string{"version": "1.20.1"}
			require.NoError(t, r.Render(env))

			require.Equal(t, c.want, buf.String())
		})
	}
}

// A JSON null exists in the envelope and is not nothing: it comes out as
// "null", never as an empty line.
func TestFieldPrintsNullAsNull(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Field: "data"}

	require.NoError(t, r.Render(output.New("status")))

	require.Equal(t, "null\n", buf.String())
}

// A digit segment indexes a list: this is what makes the error path readable
// with `--field diagnostics.0.code`.
func TestFieldIndexesLists(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Field: "diagnostics.0.code"}

	env := output.New("test")
	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityError, Code: "NGX-0003", Message: "invalid"})
	require.NoError(t, r.Render(env))

	require.Equal(t, "NGX-0003\n", buf.String())
}

// THE test of this feature. A path that does not exist is a usage error with
// NOTHING on stdout. An empty line here would be assigned by
// `V=$(ngx --field x.y status)` and the script would carry on believing it
// worked. Asserting only the exit code would pass with the defect present,
// which is why stdout is asserted too.
func TestFieldWithMissingPathIsUsageErrorAndWritesNothing(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"key that does not exist", "meta.nginx_version"},
		{"key that does not exist at the top level", "nope"},
		{"index out of range", "diagnostics.7.code"},
		{"index that is not a number", "diagnostics.first"},
		{"path going through a scalar", "command.length"},
		{"path going through a null", "data.anything"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Field: c.field}

			err := r.Render(output.New("status"))

			require.Error(t, err)
			require.Equal(t, output.ExitUsage, output.CodeOf(err))
			require.Empty(t, buf.String(), "an empty line would become an empty shell variable")
		})
	}
}

// Redaction happens before the selection: asking for the field of a secret
// returns the redacted value, it does not bypass the protection.
func TestFieldAppliesRedaction(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, Redact: set, Field: "data.value"}

	env := output.New("get")
	env.Data = redactableData{Value: "/etc/ssl/priv.key"}
	require.NoError(t, r.Render(env))

	require.Equal(t, output.RedactedValue+"\n", buf.String())
}

// The --no-redact gate comes first: --field is not a way around it.
func TestFieldDoesNotBypassTheNoRedactGate(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, NoRedact: true, Field: "ok"}

	err := r.Render(output.New("get"))

	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
	require.Empty(t, buf.String())
}

// In the renderer, Field takes precedence over the format and over Quiet: the
// conflict between --field and the flags that choose the presentation is
// decided at the flag layer (prepare), because output.format also comes from
// the configuration file, where it is an ambient default and not an explicit
// request. Whatever reaches the renderer with Field filled in wants one value.
func TestFieldTakesPrecedenceOverFormatAndQuiet(t *testing.T) {
	for _, format := range []output.Format{output.FormatJSON, output.FormatHuman, output.FormatAuto} {
		var buf bytes.Buffer
		r := &output.Renderer{Out: &buf, Format: format, IsTTY: true, Quiet: true, Field: "command"}

		require.NoError(t, r.Render(output.New("status")))

		require.Equal(t, "status\n", buf.String())
	}
}
