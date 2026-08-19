package output

import (
	"bytes"
	"fmt"

	"github.com/itchyny/gojq"
)

// ValidateQuery reports whether a --query expression parses. It exists so the
// flag layer can refuse a typo BEFORE opening an SSH connection, and before
// the renderer is told about the expression -- an invalid expression that
// reached the renderer would come back out through renderQuery's own error
// path, which writes no envelope at all.
//
// The message is gojq's, not ours: it already names the offending token and
// the byte offset, and rewriting it would make it worse.
func ValidateQuery(expr string) error {
	if _, err := gojq.Parse(expr); err != nil {
		return Usage("--query: %s", err.Error())
	}
	return nil
}

// renderQuery applies the jq expression to the envelope and writes one line
// per result.
//
// The envelope it receives is the one that WOULD have been printed --
// redaction already applied by Render. That is the whole security property of
// this flag: running the expression against the in-memory tree would read the
// real value and turn --query into a way around the redactor, so
// `--query '.data.config[0].parsed[0].args'` would hand out the private key
// that *** exists to hide.
//
// Nothing reaches stdout until every result has been produced. gojq can fail
// halfway through (an expression that indexes a string, for example), and a
// failure that has already written half its lines leaves the caller with a
// truncated answer and an exit code that says nothing about where it stopped.
// The envelope is small enough that buffering it costs nothing.
func (r *Renderer) renderQuery(env *Envelope) error {
	doc, err := envelopeDocument(env)
	if err != nil {
		return err
	}

	// Parsed again here, even though the flag layer already validated it:
	// the renderer is usable on its own (that is what its tests exercise)
	// and a *gojq.Query in the struct would make it depend on somebody
	// else having compiled it first. Parsing an expression of a few dozen
	// bytes is not a cost worth that coupling.
	query, err := gojq.Parse(r.Query)
	if err != nil {
		return Usage("--query: %s", err.Error())
	}

	var buf bytes.Buffer
	iter := query.Run(doc)
	for {
		value, ok := iter.Next()
		if !ok {
			break
		}
		// A runtime failure of the expression -- including `halt` and
		// `halt_error` -- is a usage error, exit 2. The query never gets
		// to choose ngx's exit code: that code says what happened to the
		// nginx operation, and letting an expression overwrite it would
		// make a successful read indistinguishable from a failed one.
		if qerr, isErr := value.(error); isErr {
			return Usage("--query: %s", qerr.Error())
		}
		text, terr := fieldText(value)
		if terr != nil {
			return terr
		}
		if _, werr := fmt.Fprintln(&buf, text); werr != nil {
			return Internal(werr, "failed to write the output")
		}
	}

	// Zero results is exit 0 with a byte-empty stdout, and it is a
	// different case from --field's missing path even though they look
	// alike from the shell.
	//
	// In jq's semantics a wrong path yields `null` -- a line -- not
	// nothing; nothing is only ever produced by a deliberate filter
	// (`select`, `empty`, iterating an empty list). So an empty result here
	// means the filter excluded everything, which is an answer: "no server
	// matches". Failing on it would turn every legitimate zero-match query
	// into an error in a `set -e` script.
	//
	// The contract that makes it readable is one line per value: zero lines
	// means zero values. A caller that needs a value even in the empty case
	// wraps the expression -- `--query '[ ... ] | length'` always prints a
	// number.
	if _, err := r.Out.Write(buf.Bytes()); err != nil {
		return Internal(err, "failed to write the output")
	}
	return nil
}
