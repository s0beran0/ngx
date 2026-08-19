package output_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// The answer to H4, asserted. An unescaped tab inside a field produces one
// extra column and the consumer reads a shifted row with no error at all --
// the failure that is worse than a refusal, because it never announces
// itself.
func TestEscapeTSV(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"an ordinary argument is untouched", "/etc/ssl/private/api.key", "/etc/ssl/private/api.key"},
		{"a tab becomes an escape, never a column", "a\tb", `a\tb`},
		{"a newline becomes an escape, never a row", "a\nb", `a\nb`},
		{"a carriage return too", "a\rb", `a\rb`},
		{"the backslash escapes itself", `a\b`, `a\\b`},
		{"quotes are bytes: TSV has no quoting", `"a b"`, `"a b"`},
		{
			"a literal backslash-t stays tellable from a real tab",
			`a\tb`,
			`a\\tb`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, output.EscapeTSV(c.in))
		})
	}

	// The property the rule exists for: after escaping, the only tab left in
	// the line is the one separating the columns.
	require.Equal(t, 1, strings.Count(
		output.EscapeTSV("a\tb")+"\t"+output.EscapeTSV("c\td"), "\t"))
}

func TestApplySubstitutions(t *testing.T) {
	src := []byte("ssl_certificate_key /etc/ssl/private/api.key;\n")

	t.Run("replaces the whole lexeme", func(t *testing.T) {
		got, err := output.ApplySubstitutions(src, 0, len(src),
			[]output.Replacement{{Start: 20, End: 44, Text: "***"}})
		require.NoError(t, err)
		require.Equal(t, "ssl_certificate_key ***;\n", string(got))
	})

	t.Run("cuts the window and keeps the offsets absolute", func(t *testing.T) {
		got, err := output.ApplySubstitutions(src, 0, 45,
			[]output.Replacement{{Start: 20, End: 44, Text: "***"}})
		require.NoError(t, err)
		require.Equal(t, "ssl_certificate_key ***;", string(got))
	})

	t.Run("orders the replacements it is given", func(t *testing.T) {
		got, err := output.ApplySubstitutions([]byte("abcdef"), 0, 6, []output.Replacement{
			{Start: 4, End: 5, Text: "E"},
			{Start: 1, End: 2, Text: "B"},
		})
		require.NoError(t, err)
		require.Equal(t, "aBcdEf", string(got))
	})

	// Each of these means the spans and the source do not describe the same
	// file. The output of a wrong cut is a configuration with a secret half
	// visible in it, so every one of them refuses instead of doing its best.
	t.Run("refuses overlapping replacements", func(t *testing.T) {
		_, err := output.ApplySubstitutions([]byte("abcdef"), 0, 6, []output.Replacement{
			{Start: 1, End: 4, Text: "X"},
			{Start: 3, End: 5, Text: "Y"},
		})
		require.Error(t, err)
		require.Equal(t, output.ExitInternal, output.CodeOf(err))
	})

	t.Run("refuses a replacement outside the window", func(t *testing.T) {
		_, err := output.ApplySubstitutions([]byte("abcdef"), 0, 3,
			[]output.Replacement{{Start: 4, End: 5, Text: "X"}})
		require.Error(t, err)
	})

	t.Run("refuses an invalid window", func(t *testing.T) {
		_, err := output.ApplySubstitutions([]byte("abcdef"), 4, 2, nil)
		require.Error(t, err)
	})
}

// nginxData is data that knows how to be nginx text but does NOT know how to
// redact itself.
type nginxData struct{ text string }

func (d nginxData) RenderNginx(w io.Writer) error {
	_, err := io.WriteString(w, d.text)
	return err
}

type tableData struct{ table output.Table }

func (d tableData) Table() (output.Table, error) { return d.table, nil }

// The gate that keeps --format nginx from being a flag that goes around the
// redactor. Redaction of the source text works by substituting the bytes the
// Redactable copy marks; data that produces no such copy while rules are
// active would emit the source whole, secrets included.
func TestRenderNginxRefusesDataThatCannotBeRedacted(t *testing.T) {
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	var out bytes.Buffer
	r := &output.Renderer{Out: &out, Format: output.FormatNginx, Redact: set}
	env := &output.Envelope{Command: "inspect", OK: true, Data: nginxData{text: "ssl_certificate_key /k;\n"}}

	err = r.Render(env)
	require.Error(t, err)
	require.Equal(t, output.ExitInternal, output.CodeOf(err))
	require.Empty(t, out.String(), "nothing is written on the refusal path")
}

// With no rules active there is nothing to substitute and the text goes out.
func TestRenderNginxWritesTheTextWhenThereIsNoRedactionToDo(t *testing.T) {
	var out bytes.Buffer
	r := &output.Renderer{Out: &out, Format: output.FormatNginx}
	env := &output.Envelope{Command: "inspect", OK: true, Data: nginxData{text: "events {}\n"}}

	require.NoError(t, r.Render(env))
	require.Equal(t, "events {}\n", out.String())
}

func TestRenderTableWritesTSV(t *testing.T) {
	var out bytes.Buffer
	r := &output.Renderer{Out: &out, Format: output.FormatTable}
	env := &output.Envelope{Command: "get", OK: true, Data: tableData{table: output.Table{
		Header: []string{"id", "value"},
		Rows:   [][]string{{"h.s0", "a\tb"}},
	}}}

	require.NoError(t, r.Render(env))
	require.Equal(t, "id\tvalue\nh.s0\ta\\tb\n", out.String())
}

// A row with the wrong number of fields is the same failure the escaping rule
// exists to prevent: a shifted line the consumer reads without an error.
// Padding or truncating it would be inventing data to keep the shape.
func TestRenderTableRefusesARowThatDoesNotMatchTheHeader(t *testing.T) {
	var out bytes.Buffer
	r := &output.Renderer{Out: &out, Format: output.FormatTable}
	env := &output.Envelope{Command: "get", OK: true, Data: tableData{table: output.Table{
		Header: []string{"id", "value"},
		Rows:   [][]string{{"h.s0"}},
	}}}

	err := r.Render(env)
	require.Error(t, err)
	require.Equal(t, output.ExitInternal, output.CodeOf(err))
}

// Neither format is ever chosen by the TTY: they answer a particular
// question, they are not a presentation default.
func TestNginxAndTableAreNeverChosenAutomatically(t *testing.T) {
	for _, isTTY := range []bool{true, false} {
		var out bytes.Buffer
		r := &output.Renderer{Out: &out, Format: output.FormatAuto, IsTTY: isTTY}
		require.NoError(t, r.Render(&output.Envelope{Command: "version", OK: true, Data: map[string]string{"version": "1"}}))
		require.NotContains(t, out.String(), "\t")
	}
}

// Data that is not flat and not nginx text is refused by name, never
// serialized as JSON as if the flag had not been typed.
func TestFormatsRefuseDataTheyCannotRepresent(t *testing.T) {
	for _, format := range []output.Format{output.FormatNginx, output.FormatTable} {
		var out bytes.Buffer
		r := &output.Renderer{Out: &out, Format: format}
		err := r.Render(&output.Envelope{Command: "status", OK: true, Data: map[string]string{"a": "b"}})
		require.Error(t, err, string(format))
		require.Equal(t, output.ExitUsage, output.CodeOf(err), string(format))
		require.Empty(t, out.String(), string(format))
	}
}
