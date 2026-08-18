# ngx v0.1 — Plan 1: Foundation and Tree

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `ngx` foundation — JSON envelope, exit codes, redaction, configuration — and the canonical nginx configuration tree with byte spans, stable IDs and hashing, delivering the `ngx inspect` command working end-to-end.

**Architecture:** `nginx-go-crossplane` provides the semantic tree and directive validation; a proprietary tokenizer provides byte offsets; the two structures are matched by sequence of tokens in document order. The `output` layer is the only one that serializes and the only one that decides the exit code; commands return typed values and typed errors.

**Tech Stack:** Go 1.25, `nginx-go-crossplane`, `cobra`, `koanf/v2`, `testify`.

**Spec:** `docs/superpowers/specs/2026-08-17-ngx-cli-design.md`

## Global Constraints

- MIT License in the name of Eduardo Benck. Every external invocation uses `exec.Command` with explicit argv.
Without any mention, branding or copyright of SEA Tecnologia.
- Every JSON list field serializes as `[]`, never `null` — an agent that does `.length` on a null list breaks it.
- Go module: `github.com/s0beran0/ngx`. - Unknown or unavailable field is **omitted**, never estimated or filled in with a false value.
- **Commit messages never mention Claude or IA.** No `Co-Authored-By` trailer, no "Generated with". Go 1.25 (`.tool-versions` already fixes `golang 1.25.9`).
- Code comments in Portuguese, like the rest of the project.Exclusive authorship by Eduardo.
- **Zero CGO.** No dependencies that require cgo.
- No shell `exec`. 

---

### Task 1: Module bootstrap and output envelope

- Test: `internal/output/envelope_test.go`**Files:**
- Create: `go.mod`, `LICENSE`, `internal/output/envelope.go`

- Produces: `output.Envelope`, `output.Diagnostic`, `output.Meta`, `output.Severity`, `output.New(command string) *Envelope`, `(*Envelope).AddDiagnostic(Diagnostic)`, `output.Version`**Interfaces:**
- Consumptions: nothing (first task)

- [ ] **Step 1: Initialize the module and license**

```bash
go mod init github.com/s0beran0/ngx
go get github.com/stretchr/testify@latest
```

Create `LICENSE` with the default text MIT, `Copyright (c) 2026 Eduardo Benck`.

- [ ] **Step 2: Write the test that fails**

Create `internal/output/envelope_test.go`:

```go
package output_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// An agent consuming the output does `.diagnostics.length`. A null list
// breaks this access, so empty list needs to serialize as [].
func TestEnvelopeSerializesEmptyDiagnosticsAsArray(t *testing.T) {
	env := output.New("status")

	b, err := json.Marshal(env)
	require.NoError(t, err)

	require.Contains(t, string(b), `"diagnostics":[]`)
	require.NotContains(t, string(b), `"diagnostics":null`)
}

func TestEnvelopeStartsOK(t *testing.T) {
	env := output.New("status")

	require.True(t, env.OK)
	require.Equal(t, "status", env.Command)
	require.NotEmpty(t, env.NgxVersion)
}

// Only the error severity clears ok in the envelope. Warning and info do not.
func TestAddDiagnosticErrorClearsOK(t *testing.T) {
	env := output.New("test")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityWarning, Message: "careful"})
	require.True(t, env.OK, "a warning must not bring ok down")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityError, Message: "failed"})
	require.False(t, env.OK, "error must clear ok")

	require.Len(t, env.Diagnostics, 2)
}

// Missing optional fields should not pollute the agent's output.
func TestDiagnosticOmitsEmptyFields(t *testing.T) {
	b, err := json.Marshal(output.Diagnostic{
		Severity: output.SeverityError,
		Code:     "NGX-0002",
		Message:  "malformed selector",
	})
	require.NoError(t, err)

	s := string(b)
	require.False(t, strings.Contains(s, `"file"`), "empty file must be omitted")
	require.False(t, strings.Contains(s, `"line"`), "zero line must be omitted")
	require.False(t, strings.Contains(s, `"selector"`), "empty selector must be omitted")
}
```

- [ ] **Step 3: Run the test to verify that it fails**

Run: `go test ./internal/output/ -run TestEnvelope -v`
Expected: FAIL — package `internal/output` does not exist yet.

- [ ] **Step 4: Write the minimum implementation**

Create `internal/output/envelope.go`:

```go
// Package output defines the ngx output envelope, the diagnostics and the
// error translation for exit code. It is the only layer that serializes.
package output

// Version is the version of ngx. Overridden in build via -ldflags.
var Version = "0.1.0-dev"

// Severity classifies a diagnostic. Only SeverityError clears the ok
// of the envelope.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Diagnostic is a located finding. The selector and id fields exist so that
// the agent can act on the finding without rereading the configuration.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Column   int      `json:"column,omitempty"`
	Selector string   `json:"selector,omitempty"`
	ID       string   `json:"id,omitempty"`
	Docs     string   `json:"docs,omitempty"`
}

// Meta carries data about the run. ConfigHash anchors the IDs returned
// in this response: an ID is only valid against the hash that came with it.
type Meta struct {
	DurationMS   int64  `json:"duration_ms"`
	NginxVersion string `json:"nginx_version,omitempty"`
	ConfigHash   string `json:"config_hash,omitempty"`
}

// Envelope is the unique format of all JSON output from ngx.
type Envelope struct {
	OK          bool         `json:"ok"`
	Command     string       `json:"command"`
	NgxVersion  string       `json:"ngx_version"`
	Data        any          `json:"data"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Meta        Meta         `json:"meta"`
}

// New creates a success envelope for the given command.
func New(command string) *Envelope {
	return &Envelope{
		OK:          true,
		Command:     command,
		NgxVersion:  Version,
		Diagnostics: []Diagnostic{},
	}
}

// AddDiagnostic appends a diagnostic, clearing ok when it is an error.
func (e *Envelope) AddDiagnostic(d Diagnostic) {
	e.Diagnostics = append(e.Diagnostics, d)
	if d.Severity == SeverityError {
		e.OK = false
	}
}
```

- [ ] **Step 5: Run the tests to check that they pass**

Run: `go test ./internal/output/ -v`
Expected: PASS — 4 tests.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum LICENSE internal/output/
git commit -m "feat(output): output envelope and diagnostics"
```

---

### Task 2: Typed errors and exit codes

- Test: `internal/output/errors_test.go`**Files:**
- Create: `internal/output/errors.go`

**Interfaces:**
- Consumes: `output.Diagnostic`, `output.SeverityError` (Task 1)
- Produces: `output.ExitCode` and the constants `ExitOK`/`ExitInternal`/`ExitUsage`/`ExitInvalidConfig`/`ExitDrift`/`ExitHashMismatch`; `output.Error` with fields `Code`/`Diag`/`Err`; constructors `output.Usage`, `output.Internal`, `output.InvalidConfig`, `output.Drift`, `output.HashMismatch`; `output.CodeOf(error) ExitCode`

- [ ] **Step 1: Write the test that fails**

Create `internal/output/errors test.go`:

```go
package output_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

func TestCodeOfNilIsSuccess(t *testing.T) {
	require.Equal(t, output.ExitOK, output.CodeOf(nil))
}

// An error that does not load code is an internal error, not a success.
func TestCodeOfUnknownErrorIsInternal(t *testing.T) {
	require.Equal(t, output.ExitInternal, output.CodeOf(errors.New("boom")))
}

func TestConstructorsCarryTheirCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want output.ExitCode
	}{
		{"usage", output.Usage("malformed selector: %q", "http..server"), output.ExitUsage},
		{"invalid config", output.InvalidConfig("nginx -t failed"), output.ExitInvalidConfig},
		{"drift", output.Drift("on-disk config changed after the last reload"), output.ExitDrift},
		{"hash", output.HashMismatch("sha256:aa", "sha256:bb"), output.ExitHashMismatch},
		{"internal", output.Internal(errors.New("io"), "failed to read"), output.ExitInternal},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, output.CodeOf(c.err))
		})
	}
}

// The code has to survive wrapping; otherwise an error wrapped by an
// intermediate layer silently turns into exit 1.
func TestCodeOfTraversesWrapping(t *testing.T) {
	err := fmt.Errorf("loading configuration: %w", output.Usage("invalid flag"))

	require.Equal(t, output.ExitUsage, output.CodeOf(err))
}

// Every typed error must yield a diagnostic the agent can display.
func TestErrorExposesDiagnostic(t *testing.T) {
	err := output.Usage("malformed selector: %q", "http..server")

	var e *output.Error
	require.True(t, errors.As(err, &e))
	require.Equal(t, output.SeverityError, e.Diag.Severity)
	require.Equal(t, "NGX-0002", e.Diag.Code)
	require.Contains(t, e.Diag.Message, "http..server")
}

// HashMismatch is the error that prevents the agent from acting on an aged ID.
// The message needs to show both hashes so he knows what happened.
func TestHashMismatchShowsBothHashes(t *testing.T) {
	err := output.HashMismatch("sha256:esperado", "sha256:atual")

	require.Contains(t, err.Error(), "sha256:esperado")
	require.Contains(t, err.Error(), "sha256:atual")
}
```

- [ ] **Step 2: Run the test to verify that it fails**

Run: `go test ./internal/output/ -run "TestCodeOf|TestConstructors|TestError|TestHashMismatch" -v`
Expected: FAIL — `undefined: output.ExitOK`, `undefined: output.CodeOf` etc.

- [ ] **Step 3: Write the minimum implementation**

Create `internal/output/errors.go`:

```go
package output

import (
	"errors"
	"fmt"
)

// ExitCode is the process exit code. v0.1 only issues the codes
// below; 4 (lint), 5 and 6 (apply) and 8 (ambiguous mutation) belong to
// commands that do not exist yet and are not documented as supported until
// they can actually be issued.
type ExitCode int

const (
	ExitOK            ExitCode = 0
	ExitInternal      ExitCode = 1
	ExitUsage         ExitCode = 2
	ExitInvalidConfig ExitCode = 3
	ExitDrift         ExitCode = 7
	ExitHashMismatch  ExitCode = 9
)

// Error is an error that carries its own exit code and the matching
// diagnostic. Commands never pick an exit code directly: they return one of
// these, and main.go translates it in a single place.
type Error struct {
	Code ExitCode
	Diag Diagnostic
	Err  error
}

func (e *Error) Error() string { return e.Diag.Message }

func (e *Error) Unwrap() error { return e.Err }

func newError(code ExitCode, diagCode, format string, args ...any) *Error {
	return &Error{
		Code: code,
		Diag: Diagnostic{
			Severity: SeverityError,
			Code:     diagCode,
			Message:  fmt.Sprintf(format, args...),
		},
	}
}

// Usage signals a usage error: invalid flag, malformed selector, missing
// required argument.
func Usage(format string, args ...any) *Error {
	return newError(ExitUsage, "NGX-0002", format, args...)
}

// InvalidConfig signals that the nginx configuration is not valid.
func InvalidConfig(format string, args ...any) *Error {
	return newError(ExitInvalidConfig, "NGX-0003", format, args...)
}

// Drift signals that the configuration on disk is different from the one loaded.
func Drift(format string, args ...any) *Error {
	return newError(ExitDrift, "NGX-0007", format, args...)
}

// HashMismatch signals that an ID has been presented against a version of the
// configuration different from the one in which it was generated. The previous IDs are
// invalid and the agent needs to reread it before acting.
func HashMismatch(expected, actual string) *Error {
	return newError(ExitHashMismatch, "NGX-0009",
		"the configuration changed since it was read: expected %s, actual %s", expected, actual)
}

// Internal involves an IO failure or a defect in ngx itself.
func Internal(err error, format string, args ...any) *Error {
	e := newError(ExitInternal, "NGX-0001", format, args...)
	e.Err = err
	return e
}

// CodeOf extracts the exit code from an error, traversing wrapping. An error
// without a code is treated as an internal failure, never as success.
func CodeOf(err error) ExitCode {
	if err == nil {
		return ExitOK
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ExitInternal
}
```

- [ ] **Step 4: Run the tests to check that they pass**

Run: `go test ./internal/output/ -v`
Expected: PASS — all Task 1 and Task 2 tests.

- [ ] **Step 5: Commit**

```bash
git add internal/output/errors.go internal/output/errors_test.go
git commit -m "feat(output): typed errors carrying their exit code"
```

---

### Task 3: Writing sensitive values

- Test: `internal/output/redact_test.go`**Files:**
- Create: `internal/output/redact.go`

**Interfaces:**
- Consumptions: no previous tasks
- Produces: `output.RedactRule` with `Matches(directive string, args []string) bool`; `output.RedactSet` with `Matches(directive string, args []string) bool` and `Empty() bool`; `output.ParseRedactRule(string) (RedactRule, error)`; `output.NewRedactSet([]string) (RedactSet, error)`; `output.RedactedValue` (the constant `"***"`); the interface `output.Redactable { Redacted(RedactSet) any }`

- [ ] **Step 1: Write the test that fails**

Create `internal/output/redact_test.go`:

```go
package output_test

import (
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// The spec uses three formats for the same thing. All of them have to work as
// written, so that a configuration copied from the spec does not fail silently.
func TestParseRedactRuleAcceptsTheThreeSpecFormats(t *testing.T) {
	cases := []struct {
		input       string
		wantDir     string
		wantArgPref []string
	}{
		{"ssl_certificate_key", "ssl_certificate_key", nil},
		{"proxy_set_header Authorization", "proxy_set_header", []string{"Authorization"}},
		{"**.auth_basic_user_file", "auth_basic_user_file", nil},
	}

	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			r, err := output.ParseRedactRule(c.input)
			require.NoError(t, err)
			require.Equal(t, c.wantDir, r.Directive)
			require.Equal(t, c.wantArgPref, r.ArgPrefix)
		})
	}
}

func TestParseRedactRuleRejectsEmptyEntry(t *testing.T) {
	_, err := output.ParseRedactRule("   ")
	require.Error(t, err)
}

func TestRuleMatchesByDirectiveName(t *testing.T) {
	r, err := output.ParseRedactRule("ssl_certificate_key")
	require.NoError(t, err)

	require.True(t, r.Matches("ssl_certificate_key", []string{"/etc/ssl/priv.key"}))
	require.False(t, r.Matches("ssl_certificate", []string{"/etc/ssl/pub.crt"}))
}

func TestRuleWithArgPrefixRequiresTheArgs(t *testing.T) {
	r, err := output.ParseRedactRule("proxy_set_header Authorization")
	require.NoError(t, err)

	require.True(t, r.Matches("proxy_set_header", []string{"Authorization", "Bearer xyz"}))
	require.False(t, r.Matches("proxy_set_header", []string{"Host", "$host"}),
		"another header must not be redacted")
	require.False(t, r.Matches("proxy_set_header", nil),
		"with no args it cannot match a rule that requires a prefix")
}

func TestRedactSetMatchesAnyRule(t *testing.T) {
	set, err := output.NewRedactSet([]string{
		"ssl_certificate_key",
		"proxy_set_header Authorization",
	})
	require.NoError(t, err)

	require.False(t, set.Empty())
	require.True(t, set.Matches("ssl_certificate_key", []string{"/k.pem"}))
	require.True(t, set.Matches("proxy_set_header", []string{"Authorization", "Bearer x"}))
	require.False(t, set.Matches("listen", []string{"443", "ssl"}))
}

func TestEmptyRedactSetMatchesNothing(t *testing.T) {
	set, err := output.NewRedactSet(nil)
	require.NoError(t, err)

	require.True(t, set.Empty())
	require.False(t, set.Matches("ssl_certificate_key", []string{"/k.pem"}))
}

func TestNewRedactSetPropagatesInvalidRule(t *testing.T) {
	_, err := output.NewRedactSet([]string{"ok_directive", ""})
	require.Error(t, err)
}
```

- [ ] **Step 2: Run the test to verify that it fails**

Run: `go test ./internal/output/ -run Redact -v`
Expected: FAIL — `undefined: output.ParseRedactRule`.

- [ ] **Step 3: Write the minimum implementation**

Create `internal/output/redact.go`:

```go
package output

import (
	"fmt"
	"strings"
)

// RedactedValue overrides the value of a sensitive directive. The directive, the id
// and the line remain visible: disappearing the entire node would make the agent
// conclude that the directive does not exist, which is worse than hiding the value.
const RedactedValue = "***"

// Redactable is implemented by any data that knows how to produce a redacted
// copy of itself. Redaction happens at serialization time, never on the
// in-memory tree: if the tree were redacted at parse time, fmt would write
// *** into the user's .conf.
type Redactable interface {
	Redacted(rs RedactSet) any
}

// RedactRule matches a directive by name, optionally requiring an argument
// prefix.
type RedactRule struct {
	Directive string
	ArgPrefix []string
}

// ParseRedactRule reads an input from output.redact. Accepts all three formats
// the spec uses: directive name, name plus argument prefix, and the context
// prefix "**." — which is redundant, because rules already apply in any
// context, but is accepted so that configurations written from the spec do
// not break.
func ParseRedactRule(s string) (RedactRule, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "**.")

	fields := strings.Fields(s)
	if len(fields) == 0 {
		return RedactRule{}, fmt.Errorf("empty redaction rule")
	}

	r := RedactRule{Directive: fields[0]}
	if len(fields) > 1 {
		r.ArgPrefix = fields[1:]
	}
	return r, nil
}

// Matches reports whether the given directive must have its value redacted.
func (r RedactRule) Matches(directive string, args []string) bool {
	if r.Directive != directive {
		return false
	}
	if len(args) < len(r.ArgPrefix) {
		return false
	}
	for i, p := range r.ArgPrefix {
		if args[i] != p {
			return false
		}
	}
	return true
}

// RedactSet is the set of active rules.
type RedactSet struct {
	rules []RedactRule
}

// NewRedactSet compiles the entries from output.redact.
func NewRedactSet(entries []string) (RedactSet, error) {
	var set RedactSet
	for _, e := range entries {
		r, err := ParseRedactRule(e)
		if err != nil {
			return RedactSet{}, fmt.Errorf("redaction rule %q: %w", e, err)
		}
		set.rules = append(set.rules, r)
	}
	return set, nil
}

// Empty reports whether no rule is active.
func (s RedactSet) Empty() bool { return len(s.rules) == 0 }

// Matches reports whether any rule matches the given directive.
func (s RedactSet) Matches(directive string, args []string) bool {
	for _, r := range s.rules {
		if r.Matches(directive, args) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the tests to check that they pass**

Run: `go test ./internal/output/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/output/redact.go internal/output/redact_test.go
git commit -m "feat(output): redaction of sensitive values"
```

---

### Task 4: Renderers and the `--no-redact` gate

- Test: `internal/output/render_test.go`**Files:**
- Create: `internal/output/render.go`

**Interfaces:**
- Consumes: `output.Envelope` (Task 1), `output.Usage` (Task 2), `output.RedactSet`, `output.Redactable` (Task 3)
- Produces: `output.Format` with `FormatAuto`/`FormatJSON`/`FormatHuman`; `output.Renderer` with fields `Out`, `Format`, `IsTTY`, `Redact`, `NoRedact`, `Quiet` and method `Render(*Envelope) error`; the interface `output.HumanRenderable { RenderHuman(io.Writer) error }`

- [ ] **Step 1: Write the test that fails**

Create `internal/output/render_test.go`:

```go
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

type humanData struct{}

func (humanData) RenderHuman(w io.Writer) error {
	_, err := io.WriteString(w, "human output\n")
	return err
}

// Auto format without a TTY has to be JSON: that is the case of the agent reading a pipe.
func TestFormatAutoWithoutTTYProducesJSON(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatAuto, IsTTY: false}

	require.NoError(t, r.Render(output.New("status")))

	var env output.Envelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, "status", env.Command)
}

// With a TTY and no human renderer on the data, it falls back to indented JSON
// instead of printing the raw Go struct.
func TestFormatAutoWithTTYUsesHumanRenderWhenAvailable(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatAuto, IsTTY: true}

	env := output.New("status")
	env.Data = humanData{}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "human output")
}

// The gate that redaction exists to close: a human at the terminal can see the
// secret; an agent reading the pipe cannot even ask for it.
func TestNoRedactIsRefusedWithoutTTY(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, NoRedact: true}

	err := r.Render(output.New("get"))

	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
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

// Without --no-redact, the data goes through redaction before being serialized.
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

// Quiet suppresses success output but never error output: an agent needs to
// know what went wrong.
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
```

- [ ] **Step 2: Run the test to verify that it fails**

Run: `go test ./internal/output/ -run "TestFormat|TestNoRedact|TestRender|TestQuiet" -v`
Expected: FAIL — `undefined: output.Renderer`.

- [ ] **Step 3: Write the minimum implementation**

Create `internal/output/render.go`:

```go
package output

import (
	"encoding/json"
	"fmt"
	"io"
)

// Format selects the renderer. FormatAuto decides based on the TTY.
type Format string

const (
	FormatAuto  Format = "auto"
	FormatJSON  Format = "json"
	FormatHuman Format = "human"
)

// HumanRenderable is implemented by data that knows how to present itself to a
// human. Data that does not implement it falls back to indented JSON, which is
// more useful than printing the raw Go struct.
type HumanRenderable interface {
	RenderHuman(w io.Writer) error
}

// Renderer serializes the envelope. It is the only layer that writes output.
type Renderer struct {
	Out      io.Writer
	Format   Format
	IsTTY    bool
	Redact   RedactSet
	NoRedact bool
	Quiet    bool
}

// Render writes the envelope in the resolved format.
func (r *Renderer) Render(env *Envelope) error {
	if r.NoRedact && !r.IsTTY {
		return Usage("--no-redact is only accepted when the output is a terminal")
	}

	if r.Quiet && env.OK {
		return nil
	}

	if !r.NoRedact && !r.Redact.Empty() {
		if red, ok := env.Data.(Redactable); ok {
			env.Data = red.Redacted(r.Redact)
		}
	}

	switch r.resolveFormat() {
	case FormatHuman:
		return r.renderHuman(env)
	default:
		return r.renderJSON(env)
	}
}

func (r *Renderer) resolveFormat() Format {
	if r.Format == FormatAuto || r.Format == "" {
		if r.IsTTY {
			return FormatHuman
		}
		return FormatJSON
	}
	return r.Format
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
			loc = fmt.Sprintf(" %s:%d:%d", d.File, d.Line, d.Column)
		}
		if _, err := fmt.Fprintf(r.Out, "%s: %s%s\n", d.Severity, d.Message, loc); err != nil {
			return Internal(err, "failed to write diagnostic")
		}
	}

	if hr, ok := env.Data.(HumanRenderable); ok {
		if err := hr.RenderHuman(r.Out); err != nil {
			return Internal(err, "failed to render human output")
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
		return Internal(err, "failed to write output")
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to check that they pass**

Run: `go test ./internal/output/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/output/render.go internal/output/render_test.go
git commit -m "feat(output): json and human renderers with the --no-redact gate"
```

---

### Task 5: ngx configuration file

- Test: `internal/settings/settings_test.go`**Files:**
- Create: `internal/settings/settings.go`

**Interfaces:**
- Consumptions: no previous tasks
- Produces: `settings.Settings` with `Nginx settings.Nginx` (fields `Binary`, `Config`) and `Output settings.Output` (fields `Format`, `Redact []string`); `settings.Load(globalPath, localPath string) (*Settings, error)`; `settings.Defaults() *Settings`

- [ ] **Step 1: Install dependencies**

```bash
go get github.com/knadh/koanf/v2@latest
go get github.com/knadh/koanf/providers/file@latest
go get github.com/knadh/koanf/parsers/yaml@latest
```

- [ ] **Step 2: Write the test that fails**

Create `internal/settings/settings test.go`:

```go
package settings_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/settings"
	"github.com/stretchr/testify/require"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

// Without any files, the defaults need to be usable on their own.
func TestLoadWithoutFilesUsesDefaults(t *testing.T) {
	dir := t.TempDir()

	s, err := settings.Load(filepath.Join(dir, "global.yaml"), filepath.Join(dir, "local.yaml"))

	require.NoError(t, err)
	require.Equal(t, "auto", s.Output.Format)
	require.NotEmpty(t, s.Output.Redact, "redaction is on by default")
	require.Contains(t, s.Output.Redact, "ssl_certificate_key")
}

func TestLoadReadsGlobalFile(t *testing.T) {
	dir := t.TempDir()
	global := write(t, dir, "global.yaml", `
nginx:
  binary: /usr/sbin/nginx
  config: /etc/nginx/nginx.conf
output:
  format: json
`)

	s, err := settings.Load(global, filepath.Join(dir, "missing.yaml"))

	require.NoError(t, err)
	require.Equal(t, "/usr/sbin/nginx", s.Nginx.Binary)
	require.Equal(t, "json", s.Output.Format)
}

// The spec rule: the local overrides the global, key by key.
func TestLocalOverridesGlobal(t *testing.T) {
	dir := t.TempDir()
	global := write(t, dir, "global.yaml", `
nginx:
  binary: /usr/sbin/nginx
  config: /etc/nginx/nginx.conf
output:
  format: json
`)
	local := write(t, dir, "local.yaml", `
nginx:
  config: /tmp/test/nginx.conf
`)

	s, err := settings.Load(global, local)

	require.NoError(t, err)
	require.Equal(t, "/tmp/test/nginx.conf", s.Nginx.Config, "local wins")
	require.Equal(t, "/usr/sbin/nginx", s.Nginx.Binary, "a key that was not overridden survives")
	require.Equal(t, "json", s.Output.Format)
}

// A file written from the full spec contains keys from future versions.
// They need to be ignored, not turned into an error.
func TestKeysFromFutureVersionsAreIgnored(t *testing.T) {
	dir := t.TempDir()
	global := write(t, dir, "global.yaml", `
nginx:
  binary: /usr/sbin/nginx
apply:
  require_plan: true
  guardrails:
    block_listen_removal: true
snapshot:
  backend: fs
lint:
  fail_on: high
mcp:
  transport: stdio
`)

	s, err := settings.Load(global, filepath.Join(dir, "missing.yaml"))

	require.NoError(t, err)
	require.Equal(t, "/usr/sbin/nginx", s.Nginx.Binary)
}

func TestInvalidYAMLBecomesError(t *testing.T) {
	dir := t.TempDir()
	bad := write(t, dir, "bad.yaml", "nginx: [this: does: not: close")

	_, err := settings.Load(bad, filepath.Join(dir, "missing.yaml"))

	require.Error(t, err)
}

// If the user declares redact, their list replaces the default instead of
// adding to it — otherwise a default rule could never be removed.
func TestDeclaredRedactReplacesTheDefault(t *testing.T) {
	dir := t.TempDir()
	global := write(t, dir, "global.yaml", `
output:
  redact:
    - minha_diretiva_secreta
`)

	s, err := settings.Load(global, filepath.Join(dir, "missing.yaml"))

	require.NoError(t, err)
	require.Equal(t, []string{"minha_diretiva_secreta"}, s.Output.Redact)
}
```

- [ ] **Step 3: Run the test to verify that it fails**

Run: `go test ./internal/settings/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 4: Write the minimum implementation**

Create `internal/settings/settings.go`:

```go
// Package settings loads the ngx configuration file itself.
// v0.1 only reads the subset that your commands use; version keys
// future ones are ignored without error, so that a file written from the
// spec completa funcione hoje.
package settings

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Nginx points to the binary and main configuration.
type Nginx struct {
	Binary string `koanf:"binary"`
	Config string `koanf:"config"`
}

// Output controls format and redaction.
type Output struct {
	Format string   `koanf:"format"`
	Redact []string `koanf:"redact"`
}

// Settings is the effective configuration of ngx.
type Settings struct {
	Nginx  Nginx  `koanf:"nginx"`
	Output Output `koanf:"output"`
}

// Defaults returns the configuration used when no file exists. Redaction is
// on: without it, a get can leak a private key path into the context of an
// LLM running on a third-party API.
func Defaults() *Settings {
	return &Settings{
		Output: Output{
			Format: "auto",
			Redact: []string{
				"ssl_certificate_key",
				"proxy_set_header Authorization",
				"auth_basic_user_file",
			},
		},
	}
}

// Load merges the global file with the local one, the local winning key by
// key. A missing file is not an error.
func Load(globalPath, localPath string) (*Settings, error) {
	k := koanf.New(".")

	for _, p := range []string{globalPath, localPath} {
		if p == "" {
			continue
		}
		if err := k.Load(file.Provider(p), yaml.Parser()); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("loading %s: %w", p, err)
		}
	}

	s := Defaults()
	if err := k.Unmarshal("", s); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	return s, nil
}
```

- [ ] **Step 5: Run the tests to check that they pass**

Run: `go test ./internal/settings/ -v`
Expected: PASS — 6 tests.

> If `TestDeclaredRedactReplacesTheDefault` fails because the koanf concatenated the lists instead of replacing, the fix is to clear `s.Output.Redact` before `Unmarshal` when `k.Exists("output.redact")` is true, and only then restore the default if the key does not exist. Make this correction within this step; do not change the test.

- [ ] **Step 6: Commit**

```bash
git add internal/settings/ go.mod go.sum
git commit -m "feat(settings): load the ngx configuration file"
```

---

### Task 6: CLI root and exit code translation

- Test: `internal/cli/root_test.go`**Files:**
- Create: `cmd/ngx/main.go`, `internal/cli/root.go`

**Interfaces:**
- Consumes: `output.Renderer`, `output.Format`, `output.CodeOf`, `output.New`, `output.Usage`, `output.NewRedactSet` (Tasks 1–4); `settings.Load`, `settings.Settings` (Task 5)
- Produces: `cli.GlobalFlags` with fields `ConfigPath`, `JSON`, `Human`, `Quiet`, `NoColor`, `NginxBin`, `NginxVersion`, `Timeout`, `Profile`, `NoRedact`; `cli.Context` with fields `Flags *GlobalFlags`, `Settings *settings.Settings`, `Renderer *output.Renderer`; `cli.Execute(args []string, stdout, stderr io.Writer, isTTY bool) output.ExitCode`; `cli.NewRoot(ctx *Context) *cobra.Command`

- [ ] **Step 1: Install cobra**

```bash
go get github.com/spf13/cobra@latest
```

- [ ] **Step 2: Write the test that fails**

Create `internal/cli/root_test.go`:

```go
package cli_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/s0beran0/ngx/internal/cli"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

func TestInvalidFlagProducesUsageExit(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--flag-that-does-not-exist"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)
}

func TestUnknownCommandProducesUsageExit(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"nonexistent-command"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)
}

// --json and --human are mutually exclusive; asking for both is a usage
// error, not a silent precedence rule.
func TestJSONAndHumanTogetherAreUsageError(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--json", "--human", "version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)
}

func TestVersionAppearsInJSONEnvelopeWithoutTTY(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitOK, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	require.True(t, env.OK)
	require.Equal(t, "version", env.Command)
}

// The error needs to appear in the envelope, on stdout, for the agent to be able to read it.
// Writing only to stderr would force the agent to capture two streams.
func TestExecutionErrorAppearsInEnvelope(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--json", "--human", "version"}, &out, &errBuf, false)
	require.Equal(t, output.ExitUsage, code)

	var env output.Envelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &env))
	require.False(t, env.OK)
	require.NotEmpty(t, env.Diagnostics)
	require.Equal(t, "NGX-0002", env.Diagnostics[0].Code)
}
```

- [ ] **Step 3: Run the test to verify that it fails**

Run: `go test ./internal/cli/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 4: Write the minimum implementation**

Create `internal/cli/root.go`:

```go
// Package cli builds the command tree. Commands produce typed values and
// typed errors; formatting and exit code are the responsibility of output.
package cli

import (
	"io"
	"time"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/settings"
	"github.com/spf13/cobra"
)

// Default paths to ngx's own configuration file.
const (
	GlobalSettingsPath = "/etc/ngx/ngx.yaml"
	LocalSettingsPath  = ".ngx/config.yaml"
)

// GlobalFlags mirrors the spec's global flags.
type GlobalFlags struct {
	ConfigPath   string
	JSON         bool
	Human        bool
	Quiet        bool
	NoColor      bool
	NginxBin     string
	NginxVersion string
	Timeout      time.Duration
	Profile      string
	NoRedact     bool
}

// Context carries what every command needs.
type Context struct {
	Flags    *GlobalFlags
	Settings *settings.Settings
	Renderer *output.Renderer
	Command  string
}

// Execute runs the CLI and returns the exit code. Never calls os.Exit: this and
// responsibility of main, which keeps the entire CLI testable.
func Execute(args []string, stdout, stderr io.Writer, isTTY bool) output.ExitCode {
	flags := &GlobalFlags{}
	ctx := &Context{
		Flags:    flags,
		Renderer: &output.Renderer{Out: stdout, IsTTY: isTTY},
	}

	root := NewRoot(ctx)
	root.SetArgs(args)
	root.SetOut(stderr)
	root.SetErr(stderr)

	err := root.Execute()
	if err == nil {
		return output.ExitOK
	}

	// Cobra returns raw error for invalid flag and command; We treat it as usage.
	if _, ok := err.(*output.Error); !ok {
		err = output.Usage("%s", err.Error())
	}

	renderErro(ctx, stdout, isTTY, err)
	return output.CodeOf(err)
}

func renderErro(ctx *Context, stdout io.Writer, isTTY bool, err error) {
	env := output.New(commandFrom(ctx))
	var e *output.Error
	if ok := asNgxError(err, &e); ok {
		env.AddDiagnostic(e.Diag)
	} else {
		env.AddDiagnostic(output.Diagnostic{
			Severity: output.SeverityError,
			Code:     "NGX-0001",
			Message:  err.Error(),
		})
	}

	r := ctx.Renderer
	if r == nil {
		r = &output.Renderer{Out: stdout, IsTTY: isTTY}
	}
	// An error is never suppressed by --quiet nor blocked by the security gate.
	// --no-redact: the agent needs to know what went wrong.
	r.Quiet = false
	r.NoRedact = false
	_ = r.Render(env)
}

// NewRoot mounts the root command with global flags.
func NewRoot(ctx *Context) *cobra.Command {
	root := &cobra.Command{
		Use:           "ngx",
		Short:         "Operate nginx with structured output and transactional changes",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return prepare(ctx, cmd)
		},
	}

	f := ctx.Flags
	p := root.PersistentFlags()
	p.StringVarP(&f.ConfigPath, "config", "c", "", "main nginx configuration")
	p.BoolVar(&f.JSON, "json", false, "force JSON output")
	p.BoolVar(&f.Human, "human", false, "force human-readable output")
	p.BoolVarP(&f.Quiet, "quiet", "q", false, "errors only")
	p.BoolVar(&f.NoColor, "no-color", false, "turn colors off")
	p.StringVar(&f.NginxBin, "nginx-bin", "", "path to the nginx binary")
	p.StringVar(&f.NginxVersion, "nginx-version", "", "assume this nginx version")
	p.DurationVar(&f.Timeout, "timeout", 30*time.Second, "operation timeout")
	p.StringVar(&f.Profile, "profile", "", "profile of the ngx configuration file")
	p.BoolVar(&f.NoRedact, "no-redact", false, "show sensitive values (terminal only)")

	root.AddCommand(newVersionCmd(ctx))
	return root
}

func prepare(ctx *Context, cmd *cobra.Command) error {
	f := ctx.Flags
	ctx.Command = cmd.Name()

	if f.JSON && f.Human {
		return output.Usage("--json and --human are mutually exclusive")
	}

	s, err := settings.Load(GlobalSettingsPath, LocalSettingsPath)
	if err != nil {
		return output.Internal(err, "%s", err.Error())
	}
	ctx.Settings = s

	set, err := output.NewRedactSet(s.Output.Redact)
	if err != nil {
		return output.Usage("%s", err.Error())
	}

	ctx.Renderer.Format = resolveFormat(f, s)
	ctx.Renderer.Redact = set
	ctx.Renderer.NoRedact = f.NoRedact
	ctx.Renderer.Quiet = f.Quiet

	cmd.SilenceUsage = true
	return nil
}

func resolveFormat(f *GlobalFlags, s *settings.Settings) output.Format {
	switch {
	case f.JSON:
		return output.FormatJSON
	case f.Human:
		return output.FormatHuman
	default:
		return output.Format(s.Output.Format)
	}
}

func newVersionCmd(ctx *Context) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the ngx version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			env := output.New("version")
			env.Data = map[string]string{"version": output.Version}
			return ctx.Renderer.Render(env)
		},
	}
}
```

Create `internal/cli/errors.go`:

```go
package cli

import (
	"errors"

	"github.com/s0beran0/ngx/internal/output"
)

func asNgxError(err error, target **output.Error) bool {
	return errors.As(err, target)
}

// commandFrom returns the name of the command that was running, so that the
// error envelope identifies the operation that failed. Before cobra resolves
// the command — an invalid global flag, for example — there is no name, and
// the fallback is the binary itself.
func commandFrom(ctx *Context) string {
	if ctx == nil || ctx.Command == "" {
		return "ngx"
	}
	return ctx.Command
}
```

Create `cmd/ngx/main.go`:

```go
// Command ngx is the entry point. The only responsibility here is wiring and
// translating the exit code.
package main

import (
	"os"

	"github.com/s0beran0/ngx/internal/cli"
	"golang.org/x/term"
)

func main() {
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))
	code := cli.Execute(os.Args[1:], os.Stdout, os.Stderr, isTTY)
	os.Exit(int(code))
}
```

```bash
go get golang.org/x/term@latest
```

- [ ] **Step 5: Run the tests to check that they pass**

Run: `go test ./... -v`
Expected: PASS in `internal/output`, `internal/settings` and `internal/cli`.

- [ ] **Step 6: Check by hand**

```bash
go build -o /tmp/ngx ./cmd/ngx
/tmp/ngx version | head -1
/tmp/ngx --json --human version; echo "exit=$?"
```

Expected: the first line is a JSON envelope with `"command":"version"`; the second prints an envelope with `NGX-0002` and `exit=2`.

- [ ] **Step 7: Commit**

```bash
git add cmd/ internal/cli/ go.mod go.sum
git commit -m "feat(cli): root command, global flags and exit code translation"
```

---

### Task 7: The tree and parse via crossplane

- Test: `internal/config/parse_test.go`, `internal/config/testdata/simple.conf`**Files:**
- Create: `internal/config/node.go`, `internal/config/parse.go`

**Interfaces:**
- Consumes: `output.RedactSet`, `output.Redactable`, `output.RedactedValue` (Task 3)
- Produces: `config.Span` (`Start`, `End`), `config.Origin` (`File`, `Line`), `config.Node` (fields as per §4.1 of the spec), `config.File` (`Path string`, `Source []byte`, `Nodes []*Node`), `config.Tree` (`Files []*File`, `Hash string`), `config.ParseOptions` (`Path string`, `Open func(string) (io.ReadCloser, error)`), `config.Parse(ParseOptions) (*Tree, error)`, `(*Node).IsComment() bool`, `(*Node).HasBlock() bool`, `(*Tree).Walk(func(*Node) bool)`

- [ ] **Step 1: Install the crossplane and create the fixture**

```bash
go get github.com/nginxinc/nginx-go-crossplane@latest
```

Create `internal/config/testdata/simple.conf`:

```nginx
# example configuration
worker_processes auto;

events {
    worker_connections 1024;
}

http {
    server_tokens off;

    upstream backend_v1 {
        server 10.0.0.1:8080;
    }

    server {
        listen 443 ssl;
        server_name api.example.com;

        ssl_certificate_key /etc/ssl/private/api.key;

        location / {
            proxy_pass http://backend_v1;
        }

        location /api {
            proxy_pass http://backend_v1;
            add_header X-A "b; c";
        }
    }
}
```

- [ ] **Step 2: Write the test that fails**

Create `internal/config/parse_test.go`:

```go
package config_test

import (
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func parseSimple(t *testing.T) *config.Tree {
	t.Helper()
	tree, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "simple.conf"),
	})
	require.NoError(t, err)
	return tree
}

func TestParseProducesAFileWithSource(t *testing.T) {
	tree := parseSimple(t)

	require.Len(t, tree.Files, 1)
	require.NotEmpty(t, tree.Files[0].Source, "the original source has to be kept for the spans")
	require.Contains(t, tree.Files[0].Path, "simple.conf")
}

func TestParsePreservesComments(t *testing.T) {
	tree := parseSimple(t)

	var comments int
	tree.Walk(func(n *config.Node) bool {
		if n.IsComment() {
			comments++
			require.NotNil(t, n.Comment)
			require.Contains(t, *n.Comment, "example configuration")
		}
		return true
	})

	require.Equal(t, 1, comments)
}

func TestParseBuildsNestedBlocks(t *testing.T) {
	tree := parseSimple(t)

	var http *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "http" {
			http = n
			return false
		}
		return true
	})

	require.NotNil(t, http)
	require.True(t, http.HasBlock())

	var servers, upstreams int
	for _, filho := range http.Block {
		switch filho.Directive {
		case "server":
			servers++
		case "upstream":
			upstreams++
		}
	}
	require.Equal(t, 1, servers)
	require.Equal(t, 1, upstreams)
}

func TestParseKeepsArgumentsAndFile(t *testing.T) {
	tree := parseSimple(t)

	var listen *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "listen" {
			listen = n
			return false
		}
		return true
	})

	require.NotNil(t, listen)
	require.Equal(t, []string{"443", "ssl"}, listen.Args)
	require.Contains(t, listen.File, "simple.conf")
}

func TestParseMissingFileBecomesError(t *testing.T) {
	_, err := config.Parse(config.ParseOptions{Path: "testdata/does-not-exist.conf"})

	require.Error(t, err)
}

// The redaction happens at the exit: the tree in memory maintains the real value, otherwise
// fmt would write *** into the user's .conf.
func TestInMemoryTreeIsNotRedacted(t *testing.T) {
	tree := parseSimple(t)

	var achou bool
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "ssl_certificate_key" {
			achou = true
			require.Equal(t, []string{"/etc/ssl/private/api.key"}, n.Args)
		}
		return true
	})
	require.True(t, achou)
}
```

- [ ] **Step 3: Run the test to verify that it fails**

Run: `go test ./internal/config/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 4: Write the data model**

Create `internal/config/node.go`:

```go
// Package config is the canonical representation of nginx configuration: a
// semantic tree comes from nginx-go-crossplane, byte offsets come from this
// package's tokenizer, and the two are matched up by token sequence.
package config

// Span is a byte range in the source file, with End exclusive.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Len returns the size of the range in bytes.
func (s Span) Len() int { return s.End - s.Start }

// Origin records where a node came from after include resolution.
type Origin struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Node is a directive. Span covers the whole directive, including the block
// and the closing delimiter; HeadSpan covers only the name and the arguments.
// Having both is what makes a v0.2 edit a byte replacement rather than a
// re-render of the file.
type Node struct {
	Directive string   `json:"directive"`
	Args      []string `json:"args"`
	File      string   `json:"file,omitempty"`
	Line      int      `json:"line"`
	Column    int      `json:"column"`
	Span      Span     `json:"span"`
	HeadSpan  Span     `json:"head_span"`
	ID        string   `json:"id,omitempty"`
	Comment   *string  `json:"comment,omitempty"`
	Block     []*Node  `json:"block,omitempty"`
	Origin    *Origin  `json:"origin,omitempty"`

	// hasBlock distinguishes "server {}" from "server;". The Block field cannot
	// do this: an empty block is an empty slice, indistinguishable from nil
	// after serialization.
	hasBlock bool
}

// IsComment reports whether the node represents a comment.
func (n *Node) IsComment() bool { return n.Directive == "#" }

// HasBlock reports whether the node opens a block, including an empty one.
func (n *Node) HasBlock() bool { return n.hasBlock }

// File is a configuration file with its original source preserved. The source
// is needed so that spans can be resolved back into text.
type File struct {
	Path   string  `json:"file"`
	Source []byte  `json:"-"`
	Nodes  []*Node `json:"parsed"`
}

// Tree is the complete result of a parse.
type Tree struct {
	Files []*File `json:"config"`
	Hash  string  `json:"-"`
}

// Walk traverses the tree in pre-order. If fn returns false, the children of
// that node are skipped.
func (t *Tree) Walk(fn func(*Node) bool) {
	for _, f := range t.Files {
		walkNodes(f.Nodes, fn)
	}
}

func walkNodes(nodes []*Node, fn func(*Node) bool) {
	for _, n := range nodes {
		if !fn(n) {
			continue
		}
		walkNodes(n.Block, fn)
	}
}
```

- [ ] **Step 5: Write the parse**

Create `internal/config/parse.go`:

```go
package config

import (
	"fmt"
	"io"
	"os"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
)

// ParseOptions controls reading. Open exists so tests can use an in-memory
// filesystem without touching disk.
type ParseOptions struct {
	Path string
	Open func(path string) (io.ReadCloser, error)
}

func (o ParseOptions) open(path string) (io.ReadCloser, error) {
	if o.Open != nil {
		return o.Open(path)
	}
	return os.Open(path)
}

// Parse reads the configuration and returns the canonical tree. Each file is
// parsed separately, keeping its own source: include resolution is a view
// built on top of this tree, not an up-front concatenation, so that spans keep
// pointing at real offsets in real files.
func Parse(opts ParseOptions) (*Tree, error) {
	payload, err := crossplane.Parse(opts.Path, &crossplane.ParseOptions{
		ParseComments:             true,
		CombineConfigs:            false,
		SingleFile:                false,
		SkipDirectiveArgsCheck:    true,
		SkipDirectiveContextCheck: true,
		ErrorOnUnknownDirectives:  false,
		Open:                      opts.open,
	})
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", opts.Path, err)
	}

	tree := &Tree{}
	for _, cfg := range payload.Config {
		src, err := readSource(opts, cfg.File)
		if err != nil {
			return nil, err
		}
		tree.Files = append(tree.Files, &File{
			Path:   cfg.File,
			Source: src,
			Nodes:  convertDirectives(cfg.Parsed, cfg.File),
		})
	}
	return tree, nil
}

func readSource(opts ParseOptions, path string) ([]byte, error) {
	rc, err := opts.open(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer rc.Close()

	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return b, nil
}

func convertDirectives(ds crossplane.Directives, file string) []*Node {
	nodes := make([]*Node, 0, len(ds))
	for _, d := range ds {
		n := &Node{
			Directive: d.Directive,
			Args:      d.Args,
			File:      file,
			Line:      d.Line,
			Comment:   d.Comment,
			hasBlock:  d.Block != nil,
		}
		if n.Args == nil {
			n.Args = []string{}
		}
		if d.Block != nil {
			n.Block = convertDirectives(d.Block, file)
		}
		nodes = append(nodes, n)
	}
	return nodes
}
```

- [ ] **Step 6: Run the tests to check that they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — 6 tests.

> `hasBlock` comes from `d.Block != nil`. If the crossplane returns a non-nil empty slice for empty blocks and nil for simple directives, this is already correct. If you return nil for both, `TestParseBuildsNestedBlocks` still passes (the fixture blocks are not empty), but Task 9 will correct the detection using the next token. Don't invent an empty block test here.

- [ ] **Step 7: Commit**

```bash
git add internal/config/ go.mod go.sum
git commit -m "feat(config): canonical tree and parse via crossplane"
```

---

### Task 8: Tokenizer with byte offsets

- Test: `internal/config/tokens_test.go`, `internal/config/fuzz_test.go`**Files:**
- Create: `internal/config/tokens.go`

**Interfaces:**
- Consumptions: no previous tasks
- Produces: `config.TokenKind` with `TokenWord`/`TokenSemicolon`/`TokenBlockStart`/`TokenBlockEnd`/`TokenComment`; `config.Token` (`Kind`, `Value`, `Raw`, `Start`, `End`, `Line`, `Column`, `Quoted`); `config.Tokenize(src []byte) ([]Token, error)`

- [ ] **Step 1: Write the test that fails**

Create `internal/config/tokens_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"unicode"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

// The invariant that supports everything else: the text between Start and End needs
// be exactly the Raw of the token. If that's true, the spans are trustworthy.
func TestTokenSpansPointToOriginalText(t *testing.T) {
	src := []byte("server {\n    listen 443 ssl;\n}\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	for _, tok := range toks {
		require.Equal(t, tok.Raw, string(src[tok.Start:tok.End]),
			"token %q at [%d,%d)", tok.Value, tok.Start, tok.End)
	}
}

func TestTokenizeSeparatesDelimiters(t *testing.T) {
	toks, err := config.Tokenize([]byte("server {\n    listen 443;\n}\n"))
	require.NoError(t, err)

	var kinds []config.TokenKind
	var values []string
	for _, tok := range toks {
		kinds = append(kinds, tok.Kind)
		values = append(values, tok.Value)
	}

	require.Equal(t, []string{"server", "{", "listen", "443", ";", "}"}, values)
	require.Equal(t, []config.TokenKind{
		config.TokenWord, config.TokenBlockStart,
		config.TokenWord, config.TokenWord, config.TokenSemicolon,
		config.TokenBlockEnd,
	}, kinds)
}

// Quotes hide ; and { from the tokenizer. Getting this wrong breaks the whole
// alignment on the first add_header with a semicolon inside.
func TestQuotesProtectDelimiters(t *testing.T) {
	src := []byte(`add_header X-A "b; c { d }";`)

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Len(t, toks, 4)
	require.Equal(t, "add_header", toks[0].Value)
	require.Equal(t, "X-A", toks[1].Value)
	require.Equal(t, "b; c { d }", toks[2].Value, "the value comes without the quotes")
	require.Equal(t, `"b; c { d }"`, toks[2].Raw, "raw keeps the quotes")
	require.True(t, toks[2].Quoted)
	require.Equal(t, config.TokenSemicolon, toks[3].Kind)
}

func TestSingleQuotesAlsoWork(t *testing.T) {
	toks, err := config.Tokenize([]byte(`return 200 'ok; end';`))
	require.NoError(t, err)

	require.Equal(t, "ok; end", toks[2].Value)
	require.True(t, toks[2].Quoted)
}

func TestEscapeInsideQuotes(t *testing.T) {
	toks, err := config.Tokenize([]byte(`msg "says \"hi\"";`))
	require.NoError(t, err)

	require.Equal(t, `says "hi"`, toks[1].Value)
}

func TestCommentRunsToEndOfLine(t *testing.T) {
	src := []byte("# a comment; with a semicolon\nlisten 80;\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Equal(t, config.TokenComment, toks[0].Kind)
	require.Equal(t, "# a comment; with a semicolon", toks[0].Raw)
	require.Equal(t, " a comment; with a semicolon", toks[0].Value,
		"the comment value comes without the leading #")
	require.Equal(t, "listen", toks[1].Value)
}

func TestLineAndColumnAreOneBased(t *testing.T) {
	src := []byte("server {\n    listen 80;\n}\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Equal(t, 1, toks[0].Line)
	require.Equal(t, 1, toks[0].Column)

	// "listen" starts on the second line, after four spaces.
	require.Equal(t, "listen", toks[2].Value)
	require.Equal(t, 2, toks[2].Line)
	require.Equal(t, 5, toks[2].Column)
}

func TestUnclosedQuoteBecomesError(t *testing.T) {
	_, err := config.Tokenize([]byte(`msg "no end;`))

	require.Error(t, err)
}

// Coverage: every byte that is not white space belongs to some token.
func TestTokensCoverEverySignificantByte(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "simple.conf"))
	require.NoError(t, err)

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	covered := make([]bool, len(src))
	prev := 0
	for _, tok := range toks {
		require.GreaterOrEqual(t, tok.Start, prev, "tokens out of order")
		for i := tok.Start; i < tok.End; i++ {
			covered[i] = true
		}
		prev = tok.End
	}

	for i, b := range src {
		if covered[i] {
			continue
		}
		require.True(t, unicode.IsSpace(rune(b)),
			"byte %d (%q) is not covered and is not whitespace", i, string(b))
	}
}
```

Create `internal/config/fuzz_test.go`:

```go
package config_test

import (
	"testing"

	"github.com/s0beran0/ngx/internal/config"
)

// The fuzz test ensures that for any input the tokenizer accepts, the spans
// keep pointing at the real text and stay in ascending order. That is the
// property the surgical editing of v0.2 rests on.
func FuzzTokenizeSpans(f *testing.F) {
	f.Add("server { listen 80; }")
	f.Add(`add_header X "a; b";`)
	f.Add("# comment\nhttp { }")
	f.Add(`location ~ \.php$ { }`)
	f.Add("map $a $b {\n default 0;\n}")

	f.Fuzz(func(t *testing.T, s string) {
		toks, err := config.Tokenize([]byte(s))
		if err != nil {
			return
		}

		prev := 0
		for _, tok := range toks {
			if tok.Start < prev {
				t.Fatalf("token starts at %d, before the previous end %d", tok.Start, prev)
			}
			if tok.End > len(s) || tok.Start > tok.End {
				t.Fatalf("invalid span [%d,%d) for a source of %d bytes", tok.Start, tok.End, len(s))
			}
			if got := s[tok.Start:tok.End]; got != tok.Raw {
				t.Fatalf("raw %q differs from source %q at [%d,%d)", tok.Raw, got, tok.Start, tok.End)
			}
			if tok.Line < 1 || tok.Column < 1 {
				t.Fatalf("zero-based line/column: %d:%d", tok.Line, tok.Column)
			}
			prev = tok.End
		}
	})
}
```

- [ ] **Step 2: Run the test to verify that it fails**

Run: `go test ./internal/config/ -run Token -v`
Expected: FAIL — `undefined: config.Tokenize`.

- [ ] **Step 3: Write the minimum implementation**

Create `internal/config/tokens.go`:

```go
package config

import "fmt"

// TokenKind classifies a token.
type TokenKind int

const (
	// TokenWord is a directive name or an argument.
	TokenWord TokenKind = iota
	TokenSemicolon
	TokenBlockStart
	TokenBlockEnd
	TokenComment
)

// Token is a lexeme with its exact byte position. Value is the semantic
// content (without quotes, without the comment's #); Raw is the original text.
type Token struct {
	Kind   TokenKind
	Value  string
	Raw    string
	Start  int
	End    int
	Line   int
	Column int
	Quoted bool
}

type tokenizer struct {
	src    []byte
	pos    int
	line   int
	col    int
	tokens []Token
}

// Tokenize breaks the source into tokens with byte offsets. It interprets no
// directive at all: it only needs to know where each lexeme starts and ends,
// respecting quotes, escapes and comments.
func Tokenize(src []byte) ([]Token, error) {
	t := &tokenizer{src: src, line: 1, col: 1}
	for {
		t.skipSpaces()
		if t.pos >= len(t.src) {
			return t.tokens, nil
		}
		if err := t.next(); err != nil {
			return nil, err
		}
	}
}

func (t *tokenizer) skipSpaces() {
	for t.pos < len(t.src) && isSpace(t.src[t.pos]) {
		t.advance()
	}
}

func (t *tokenizer) advance() {
	if t.src[t.pos] == '\n' {
		t.line++
		t.col = 1
	} else {
		t.col++
	}
	t.pos++
}

func (t *tokenizer) next() error {
	start, line, col := t.pos, t.line, t.col

	switch c := t.src[t.pos]; {
	case c == ';':
		t.advance()
		t.emit(TokenSemicolon, ";", start, line, col, false)
		return nil
	case c == '{':
		t.advance()
		t.emit(TokenBlockStart, "{", start, line, col, false)
		return nil
	case c == '}':
		t.advance()
		t.emit(TokenBlockEnd, "}", start, line, col, false)
		return nil
	case c == '#':
		for t.pos < len(t.src) && t.src[t.pos] != '\n' {
			t.advance()
		}
		t.emit(TokenComment, string(t.src[start+1:t.pos]), start, line, col, false)
		return nil
	case c == '"' || c == '\'':
		return t.readQuoted(c, start, line, col)
	default:
		return t.readWord(start, line, col)
	}
}

func (t *tokenizer) readQuoted(quote byte, start, line, col int) error {
	t.advance() // consume the opening quote

	var value []byte
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		switch {
		case c == '\\' && t.pos+1 < len(t.src):
			t.advance()
			value = append(value, t.src[t.pos])
			t.advance()
		case c == quote:
			t.advance() // consume the closing quote
			t.emit(TokenWord, string(value), start, line, col, true)
			return nil
		default:
			value = append(value, c)
			t.advance()
		}
	}
	return fmt.Errorf("quote %q opened on line %d was never closed", string(quote), line)
}

func (t *tokenizer) readWord(start, line, col int) error {
	var value []byte
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		if isSpace(c) || c == ';' || c == '{' || c == '}' {
			break
		}
		if c == '\\' && t.pos+1 < len(t.src) {
			value = append(value, c)
			t.advance()
			value = append(value, t.src[t.pos])
			t.advance()
			continue
		}
		value = append(value, c)
		t.advance()
	}
	t.emit(TokenWord, string(value), start, line, col, false)
	return nil
}

func (t *tokenizer) emit(kind TokenKind, value string, start, line, col int, quoted bool) {
	t.tokens = append(t.tokens, Token{
		Kind:   kind,
		Value:  value,
		Raw:    string(t.src[start:t.pos]),
		Start:  start,
		End:    t.pos,
		Line:   line,
		Column: col,
		Quoted: quoted,
	})
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
```

- [ ] **Step 4: Run the tests to check that they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — 9 token tests plus 6 parse tests.

- [ ] **Step 5: Run the fuzz for 30 seconds**

Run: `go test ./internal/config/ -run FuzzTokenizeSpans -fuzz FuzzTokenizeSpans -fuzztime 30s`
Expected: `elapsed: 30s` without fail. If fuzz finds a case, it writes to `testdata/fuzz/`; fix the tokenizer and keep the case as regression.

- [ ] **Step 6: Commit**

```bash
git add internal/config/tokens.go internal/config/tokens_test.go internal/config/fuzz_test.go
git commit -m "feat(config): tokenizer with byte offsets"
```

---

### Task 9: Token↔tree alignment

**Files:**
- Create: `internal/config/align.go`
- Modify: `internal/config/parse.go` — call the alignment at the end of `Parse`
- Test: `internal/config/align_test.go`

**Interfaces:**
- Consumptions: `config.Node`, `config.File`, `config.Tree`, `config.Span` (Task 7); `config.Token`, `config.Tokenize` (Task 8)
- Produces: `config.align(f *File) error` (not exported; called by `Parse`). After Task 9, every `*Node` returned by `Parse` has `Span`, `HeadSpan`, `Line` and `Column` populated, and `HasBlock()` reflects the actual presence of `{`.

- [ ] **Step 1: Write the test that fails**

Create `internal/config/align_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func TestHeadSpanCoversDirectiveAndArguments(t *testing.T) {
	tree := parseSimple(t)
	src := tree.Files[0].Source

	var listen *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "listen" {
			listen = n
			return false
		}
		return true
	})
	require.NotNil(t, listen)

	require.Equal(t, "listen 443 ssl", string(src[listen.HeadSpan.Start:listen.HeadSpan.End]))
}

func TestSimpleDirectiveSpanEndsAtSemicolon(t *testing.T) {
	tree := parseSimple(t)
	src := tree.Files[0].Source

	var listen *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "listen" {
			listen = n
			return false
		}
		return true
	})

	require.Equal(t, "listen 443 ssl;", string(src[listen.Span.Start:listen.Span.End]))
}

func TestBlockSpanEndsAtClosingBrace(t *testing.T) {
	tree := parseSimple(t)
	src := tree.Files[0].Source

	var upstream *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "upstream" {
			upstream = n
			return false
		}
		return true
	})
	require.NotNil(t, upstream)

	text := string(src[upstream.Span.Start:upstream.Span.End])
	require.True(t, strings.HasPrefix(text, "upstream backend_v1"))
	require.True(t, strings.HasSuffix(text, "}"))
	require.Contains(t, text, "server 10.0.0.1:8080;")

	require.Equal(t, "upstream backend_v1", string(src[upstream.HeadSpan.Start:upstream.HeadSpan.End]),
		"the head does not include the block")
}

func TestLineAndColumnComeFromTokenizer(t *testing.T) {
	tree := parseSimple(t)

	var serverName *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "server_name" {
			serverName = n
			return false
		}
		return true
	})
	require.NotNil(t, serverName)

	require.Greater(t, serverName.Line, 0)
	require.Greater(t, serverName.Column, 0)

	lines := strings.Split(string(tree.Files[0].Source), "\n")
	require.Contains(t, lines[serverName.Line-1], "server_name")
}

// Quotes containing semicolons are the case that breaks a naive alignment.
func TestAlignmentSurvivesQuotesWithSemicolon(t *testing.T) {
	tree := parseSimple(t)
	src := tree.Files[0].Source

	var addHeader *config.Node
	tree.Walk(func(n *config.Node) bool {
		if n.Directive == "add_header" {
			addHeader = n
			return false
		}
		return true
	})
	require.NotNil(t, addHeader)

	require.Equal(t, `add_header X-A "b; c";`, string(src[addHeader.Span.Start:addHeader.Span.End]))
}

// Containment invariant: a child's span lives inside the parent's span.
func TestChildSpansAreContainedInParent(t *testing.T) {
	tree := parseSimple(t)

	var verify func(nodes []*config.Node, parent *config.Node)
	verify = func(nodes []*config.Node, parent *config.Node) {
		previousEnd := -1
		for _, n := range nodes {
			if parent != nil {
				require.GreaterOrEqual(t, n.Span.Start, parent.Span.Start,
					"%s starts before its parent %s", n.Directive, parent.Directive)
				require.LessOrEqual(t, n.Span.End, parent.Span.End,
					"%s ends after its parent %s", n.Directive, parent.Directive)
			}
			require.GreaterOrEqual(t, n.Span.Start, previousEnd,
				"%s overlaps the previous sibling", n.Directive)
			previousEnd = n.Span.End
			verify(n.Block, n)
		}
	}

	for _, f := range tree.Files {
		verify(f.Nodes, nil)
	}
}

// Coverage: every significant byte of the file belongs to the span of some node.
// root level. It is the concrete formulation of the property the architecture
// rests on: if it holds, the token-to-tree matching is correct.
func TestRootSpansCoverEverySignificantByte(t *testing.T) {
	tree := parseSimple(t)
	src := tree.Files[0].Source

	covered := make([]bool, len(src))
	for _, n := range tree.Files[0].Nodes {
		for i := n.Span.Start; i < n.Span.End; i++ {
			covered[i] = true
		}
	}

	for i, b := range src {
		if covered[i] {
			continue
		}
		require.True(t, b == ' ' || b == '\t' || b == '\n' || b == '\r',
			"byte %d (%q) on the uncovered line is not whitespace", i, string(b))
	}
}

func TestEmptyBlockIsRecognized(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.conf")
	require.NoError(t, os.WriteFile(p, []byte("events {}\n"), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)

	events := tree.Files[0].Nodes[0]
	require.Equal(t, "events", events.Directive)
	require.True(t, events.HasBlock(), "events {} opens a block, even an empty one")
	require.Equal(t, "events {}", string(tree.Files[0].Source[events.Span.Start:events.Span.End]))
}
```

- [ ] **Step 2: Run the test to verify that it fails**

Run: `go test ./internal/config/ -run "TestHeadSpan|TestSpan|TestLine|TestAlignment|TestBlock" -v`
Expected: FAIL — spans are all reset.

- [ ] **Step 3: Write the alignment**

Create `internal/config/align.go`:

```go
package config

import "fmt"

// align matches the semantic tree coming from crossplane with the tokens of
// the file, attaching byte offsets to every node.
//
// The match is sequential: crossplane preserves document order, and with
// ParseComments turned on even the comments are in the tree. So a single
// simultaneous walk solves everything — no search, no heuristics.
func align(f *File) error {
	toks, err := Tokenize(f.Source)
	if err != nil {
		return fmt.Errorf("tokenizing %s: %w", f.Path, err)
	}

	a := &aligner{file: f.Path, toks: toks}
	if err := a.nodes(f.Nodes); err != nil {
		return err
	}
	if a.pos != len(a.toks) {
		return fmt.Errorf("%s: %d tokens left over after aligning the tree",
			f.Path, len(a.toks)-a.pos)
	}
	return nil
}

type aligner struct {
	file string
	toks []Token
	pos  int
}

func (a *aligner) nodes(nodes []*Node) error {
	for _, n := range nodes {
		if err := a.node(n); err != nil {
			return err
		}
	}
	return nil
}

func (a *aligner) node(n *Node) error {
	if n.IsComment() {
		tok, err := a.consume(TokenComment)
		if err != nil {
			return err
		}
		n.Line, n.Column = tok.Line, tok.Column
		n.Span = Span{tok.Start, tok.End}
		n.HeadSpan = n.Span
		return nil
	}

	name, err := a.consume(TokenWord)
	if err != nil {
		return err
	}
	n.Line, n.Column = name.Line, name.Column

	headEnd := name.End
	for range n.Args {
		arg, err := a.consume(TokenWord)
		if err != nil {
			return err
		}
		headEnd = arg.End
	}
	n.HeadSpan = Span{name.Start, headEnd}

	// Looking at the next token is more reliable than inspecting n.Block: an
	// empty block is indistinguishable from a simple directive by that field.
	next, err := a.peek()
	if err != nil {
		return err
	}

	switch next.Kind {
	case TokenSemicolon:
		end, _ := a.consume(TokenSemicolon)
		n.hasBlock = false
		n.Span = Span{name.Start, end.End}
		return nil

	case TokenBlockStart:
		if _, err := a.consume(TokenBlockStart); err != nil {
			return err
		}
		if err := a.nodes(n.Block); err != nil {
			return err
		}
		end, err := a.consume(TokenBlockEnd)
		if err != nil {
			return err
		}
		n.hasBlock = true
		n.Span = Span{name.Start, end.End}
		return nil

	default:
		return fmt.Errorf("%s:%d: expected ';' or '{' after %q, found %q",
			a.file, next.Line, n.Directive, next.Raw)
	}
}

func (a *aligner) peek() (Token, error) {
	if a.pos >= len(a.toks) {
		return Token{}, fmt.Errorf("%s: unexpected end of configuration", a.file)
	}
	return a.toks[a.pos], nil
}

func (a *aligner) consume(kind TokenKind) (Token, error) {
	tok, err := a.peek()
	if err != nil {
		return Token{}, err
	}
	if tok.Kind != kind {
		return Token{}, fmt.Errorf("%s:%d:%d: unexpected token %q",
			a.file, tok.Line, tok.Column, tok.Raw)
	}
	a.pos++
	return tok, nil
}
```

- [ ] **Step 4: Call the alignment in parse**

In `internal/config/parse.go`, inside the loop over `payload.Config`, replace the block that assembles the `File` with:

```go
		file := &File{
			Path:   cfg.File,
			Source: src,
			Nodes:  convertDirectives(cfg.Parsed, cfg.File),
		}
		if err := align(file); err != nil {
			return nil, err
		}
		tree.Files = append(tree.Files, file)
```

- [ ] **Step 5: Run the tests to check that they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — all of them, including the 8 new alignment tests.

- [ ] **Step 6: Run the alignment fuzz**

Add to `internal/config/fuzz_test.go`:

```go
// Alignment must never panic nor produce a span out of bounds, whatever input
// crossplane accepted.
func FuzzAlignment(f *testing.F) {
	f.Add("server { listen 80; }")
	f.Add("http { server { location / { proxy_pass http://a; } } }")
	f.Add("# c\nevents {}")

	f.Fuzz(func(t *testing.T, s string) {
		dir := t.TempDir()
		p := filepath.Join(dir, "f.conf")
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Skip()
		}

		tree, err := config.Parse(config.ParseOptions{Path: p})
		if err != nil {
			return
		}

		for _, file := range tree.Files {
			n := len(file.Source)
			tree.Walk(func(node *config.Node) bool {
				if node.Span.Start < 0 || node.Span.End > n || node.Span.Start > node.Span.End {
					t.Fatalf("invalid span [%d,%d) for a source of %d bytes",
						node.Span.Start, node.Span.End, n)
				}
				if node.HeadSpan.Start < node.Span.Start || node.HeadSpan.End > node.Span.End {
					t.Fatalf("head span outside the node span")
				}
				return true
			})
		}
	})
}
```

Add the imports `os` and `path/filepath` to the fuzz file.

Run: `go test ./internal/config/ -run FuzzAlignment -fuzz FuzzAlignment -fuzztime 60s`
Expected: no failures. Any case found lands in `testdata/fuzz/` as a regression — commit it.

- [ ] **Step 7: Commit**

```bash
git add internal/config/
git commit -m "feat(config): token-tree alignment with byte spans"
```

---

### Task 10: Stable IDs

**Files:**
- Create: `internal/config/ids.go`
- Modify: `internal/config/parse.go` — assign IDs after alignment
- Test: `internal/config/ids_test.go`

**Interfaces:**
- Consumes: `config.Node`, `config.Tree` (Task 7)
- Produces: `config.AssignIDs(nodes []*Node, prefix string)`; `config.FindByID(t *Tree, id string) *Node`

- [ ] **Step 1: Write the test that fails**

Create `internal/config/ids_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func parseText(t *testing.T, conteudo string) *config.Tree {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "t.conf")
	require.NoError(t, os.WriteFile(p, []byte(conteudo), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)
	return tree
}

// Root-level context blocks do not have an index: they occur at most once.
func TestRootBlocksTakeNoIndex(t *testing.T) {
	tree := parseText(t, "events {}\nhttp {}\n")

	require.Equal(t, "e", tree.Files[0].Nodes[0].ID)
	require.Equal(t, "h", tree.Files[0].Nodes[1].ID)
}

func TestServersAreNumberedAmongThemselves(t *testing.T) {
	tree := parseText(t, `http {
  server { listen 80; }
  server { listen 443; }
}`)

	http := tree.Files[0].Nodes[0]
	require.Equal(t, "h.s0", http.Block[0].ID)
	require.Equal(t, "h.s1", http.Block[1].ID)
}

// The rule that reduces fragility: the index counts between siblings of the same type,
// not by absolute position. Entering a location does not renumber the servers.
func TestIndexCountsAmongSiblingsOfSameType(t *testing.T) {
	tree := parseText(t, `http {
  upstream a { server 10.0.0.1; }
  server { listen 80; }
  upstream b { server 10.0.0.2; }
  server { listen 443; }
}`)

	http := tree.Files[0].Nodes[0]
	require.Equal(t, "h.u0", http.Block[0].ID)
	require.Equal(t, "h.s0", http.Block[1].ID)
	require.Equal(t, "h.u1", http.Block[2].ID)
	require.Equal(t, "h.s1", http.Block[3].ID, "the second server is still s1")
}

func TestSimpleDirectivesUsePrefixD(t *testing.T) {
	tree := parseText(t, `http {
  server {
    listen 443 ssl;
    server_name api.example.com;
    location / { proxy_pass http://a; }
  }
}`)

	server := tree.Files[0].Nodes[0].Block[0]
	require.Equal(t, "h.s0.d0", server.Block[0].ID)
	require.Equal(t, "h.s0.d1", server.Block[1].ID)
	require.Equal(t, "h.s0.l0", server.Block[2].ID, "location has its own abbreviation")
}

// Comments get no ID and do not count toward the index: if they did, adding a
// comment would renumber the directives around it.
func TestCommentsGetNoIDAndDoNotShiftIndexes(t *testing.T) {
	tree := parseText(t, `http {
  server {
    # explains the listen
    listen 443 ssl;
    # explains the name
    server_name api.example.com;
  }
}`)

	server := tree.Files[0].Nodes[0].Block[0]

	require.Empty(t, server.Block[0].ID, "a comment has no ID")
	require.Equal(t, "h.s0.d0", server.Block[1].ID)
	require.Empty(t, server.Block[2].ID)
	require.Equal(t, "h.s0.d1", server.Block[3].ID, "the comment in between did not shift the index")
}

func TestNestedLocationsChainTheID(t *testing.T) {
	tree := parseText(t, `http {
  server {
    location /a {
      location /a/b { proxy_pass http://x; }
    }
  }
}`)

	server := tree.Files[0].Nodes[0].Block[0]
	require.Equal(t, "h.s0.l0", server.Block[0].ID)
	require.Equal(t, "h.s0.l0.l0", server.Block[0].Block[0].ID)
}

// Directives with no abbreviation in the table use the full name, which keeps
// the ID readable and avoids a collision between server and stream.
func TestDirectiveWithoutAbbreviationUsesFullName(t *testing.T) {
	tree := parseText(t, `http {
  map $a $b { default 0; }
  stream { }
}`)

	http := tree.Files[0].Nodes[0]
	require.Equal(t, "h.mp0", http.Block[0].ID)
	require.Equal(t, "h.st0", http.Block[1].ID)
}

func TestFindByIDFindsTheNode(t *testing.T) {
	tree := parseText(t, `http {
  server {
    location /api { proxy_pass http://backend; }
  }
}`)

	n := config.FindByID(tree, "h.s0.l0")

	require.NotNil(t, n)
	require.Equal(t, "location", n.Directive)
	require.Equal(t, []string{"/api"}, n.Args)
}

func TestFindByIDReturnsNilWhenNotFound(t *testing.T) {
	tree := parseText(t, "http { server { listen 80; } }")

	require.Nil(t, config.FindByID(tree, "h.s9"))
}
```

- [ ] **Step 2: Run the test to verify that it fails**

Run: `go test ./internal/config/ -run "TestBlocks|TestServers|TestIndex|TestDirectives|TestComments|TestLocations|TestDirective|TestFindByID" -v`
Expected: FAIL — `undefined: config.FindByID`, empty IDs.

- [ ] **Step 3: Write the minimum implementation**

Create `internal/config/ids.go`:

```go
package config

import (
	"fmt"
	"strings"
)

// abbreviations shortens the most common block directives. The first letter
// alone is not enough: server and stream would collide.
var abbreviations = map[string]string{
	"http":     "h",
	"stream":   "st",
	"events":   "e",
	"mail":     "m",
	"server":   "s",
	"location": "l",
	"upstream": "u",
	"map":      "mp",
}

// Root blocks are the top-level contexts, which occur at most once each. They
// need no index: the ID is "h", not "h0".
var rootBlocks = map[string]bool{
	"http":   true,
	"stream": true,
	"events": true,
	"mail":   true,
}

// AssignIDs fills in the ID field of every node, recursively.
//
// The index counts among siblings of the same directive, not by absolute
// position: inserting a location does not renumber the servers beside it.
// Comments get no ID and do not take part in the count; otherwise adding a
// comment would shift the IDs of the neighboring directives.
func AssignIDs(nodes []*Node, prefix string) {
	counters := map[string]int{}
	atRoot := prefix == ""

	for _, n := range nodes {
		if n.IsComment() {
			continue
		}

		seg := segment(n, counters, atRoot)
		if atRoot {
			n.ID = seg
		} else {
			n.ID = prefix + "." + seg
		}

		if len(n.Block) > 0 {
			AssignIDs(n.Block, n.ID)
		}
	}
}

func segment(n *Node, counters map[string]int, atRoot bool) string {
	// Only the root level skips the index: a stream nested inside http is just
	// another sibling block and has to be numbered normally.
	if atRoot && n.HasBlock() && rootBlocks[n.Directive] {
		return abbreviate(n.Directive)
	}

	key := n.Directive
	base := abbreviate(n.Directive)
	if !n.HasBlock() && abbreviations[n.Directive] == "" {
		// Simple directives without their own abbreviation share the d counter.
		key, base = "", "d"
	}

	i := counters[key]
	counters[key] = i + 1
	return fmt.Sprintf("%s%d", base, i)
}

func abbreviate(directive string) string {
	if a, ok := abbreviations[directive]; ok {
		return a
	}
	return directive
}

// FindByID locates a node by its ID. Returns nil if it does not exist.
func FindByID(t *Tree, id string) *Node {
	id = strings.TrimPrefix(id, "#")

	var found *Node
	t.Walk(func(n *Node) bool {
		if found != nil {
			return false
		}
		if n.ID == id {
			found = n
			return false
		}
		return true
	})
	return found
}
```

- [ ] **Step 4: Assign IDs in the parse**

In `internal/config/parse.go`, right after `align(file)`:

```go
		AssignIDs(file.Nodes, "")
```

- [ ] **Step 5: Run the tests to check that they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — 9 new ID tests plus all previous ones.

- [ ] **Step 6: Commit**

```bash
git add internal/config/ids.go internal/config/ids_test.go internal/config/parse.go
git commit -m "feat(config): stable IDs counted among siblings of the same type"
```

---

### Task 11: Configuration canonical hash

**Files:**
- Create: `internal/config/hash.go`
- Modify: `internal/config/parse.go` — calculate the hash at the end of `Parse`
- Test: `internal/config/hash_test.go`

- Produces: `config.Hash(t *Tree) string` (returns `"sha256:<hex>"`)**Interfaces:**
- Consumes: `config.Node`, `config.Tree` (Task 7)

- [ ] **Step 1: Write the test that fails**

Create `internal/config/hash_test.go`:

```go
package config_test

import (
	"strings"
	"testing"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func TestHashHasSha256Prefix(t *testing.T) {
	tree := parseText(t, "http { server { listen 80; } }")

	require.True(t, strings.HasPrefix(tree.Hash, "sha256:"))
	require.Len(t, tree.Hash, len("sha256:")+64)
}

func TestHashIsDeterministic(t *testing.T) {
	a := parseText(t, "http { server { listen 80; } }")
	b := parseText(t, "http { server { listen 80; } }")

	require.Equal(t, a.Hash, b.Hash)
}

// The hash protects the meaning, not the text: two configurations that differ
// only in formatting must produce the same hash, otherwise running fmt would
// invalidate every ID the agent is holding.
func TestDifferentFormattingProducesSameHash(t *testing.T) {
	compact := parseText(t, "http{server{listen 80;}}")
	spaced := parseText(t, `
http {
    server {
        listen 80;
    }
}
`)

	require.Equal(t, compact.Hash, spaced.Hash)
}

func TestCommentsDoNotEnterTheHash(t *testing.T) {
	without := parseText(t, "http { server { listen 80; } }")
	with := parseText(t, `# a comment
http {
  # another one
  server { listen 80; }
}`)

	require.Equal(t, without.Hash, with.Hash)
}

func TestArgumentChangeChangesTheHash(t *testing.T) {
	a := parseText(t, "http { server { listen 80; } }")
	b := parseText(t, "http { server { listen 443; } }")

	require.NotEqual(t, a.Hash, b.Hash)
}

// Order matters: moving a server changes what the IDs mean, so the hash has to
// change too.
func TestBlockOrderChangesTheHash(t *testing.T) {
	a := parseText(t, "http { server { listen 80; } server { listen 443; } }")
	b := parseText(t, "http { server { listen 443; } server { listen 80; } }")

	require.NotEqual(t, a.Hash, b.Hash)
}

// Without a separator between directive and arguments, "a b" and "ab" would collide.
func TestDifferentDirectivesDoNotCollide(t *testing.T) {
	a := parseText(t, "ab c;")
	b := parseText(t, "a bc;")

	require.NotEqual(t, a.Hash, b.Hash)
}
```

- [ ] **Step 2: Run the test to verify that it fails**

Run: `go test ./internal/config/ -run TestHash -v`
Expected: FAIL — empty `tree.Hash`.

- [ ] **Step 3: Write the minimum implementation**

Create `internal/config/hash.go`:

```go
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"strconv"
)

// Hash returns the canonical hash of the tree.
//
// What the hash protects is the meaning, not the text: comments and spacing
// are left out, so running fmt does not invalidate the IDs the agent is
// holding. Block order does count, because moving a server changes what each
// ID refers to.
func Hash(t *Tree) string {
	h := sha256.New()
	for _, f := range t.Files {
		writeField(h, f.Path)
		writeNodes(h, f.Nodes)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func writeNodes(h hash.Hash, nodes []*Node) {
	for _, n := range nodes {
		if n.IsComment() {
			continue
		}
		writeField(h, n.Directive)
		writeField(h, strconv.Itoa(len(n.Args)))
		for _, a := range n.Args {
			writeField(h, a)
		}
		if n.HasBlock() {
			writeField(h, "{")
			writeNodes(h, n.Block)
			writeField(h, "}")
		} else {
			writeField(h, ";")
		}
	}
}

// writeField uses a separator that cannot appear in a directive, so that
// "ab c" and "a bc" never collide.
func writeField(h hash.Hash, s string) {
	_, _ = h.Write([]byte(s))
	_, _ = h.Write([]byte{0})
}
```

- [ ] **Step 4: Calculate the hash in parse**

In `internal/config/parse.go`, before the `return tree, nil`:

```go
	tree.Hash = Hash(tree)
```

- [ ] **Step 5: Run the tests to check that they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS.

> `TestDifferentFormattingProducesSameHash` and `TestCommentsDoNotEnterTheHash` use different files in `t.TempDir()`, and the file path enters the hash via `writeField(h, f.Path)`. That makes both tests fail. The fix is to use only the **base name** of the file in the hash, not the absolute path — which is also the right behavior: moving the configuration to another directory does not change its meaning. Apply this correction in this step; do not change the tests.

- [ ] **Step 6: Commit**

```bash
git add internal/config/hash.go internal/config/hash_test.go internal/config/parse.go
git commit -m "feat(config): canonical hash anchoring the IDs"
```

---

### Task 12: Resolving include with source tracking

- Test: `internal/config/combine_test.go`, `internal/config/testdata/combine/nginx.conf`, `internal/config/testdata/combine/conf.d/api.conf`**Files:**
- Create: `internal/config/combine.go`

- Produces: `config.Combine(t *Tree) (*Tree, error)` — returns a new tree with a single `File`, where each `include` has been replaced by the included file nodes and each node carries `Origin`**Interfaces:**
- Consumes: `config.Tree`, `config.File`, `config.Node`, `config.Origin`, `config.AssignIDs` (Tasks 7, 10)

- [ ] **Step 1: Create the fixtures**

Create `internal/config/testdata/combine/nginx.conf`:

```nginx
events {}

http {
    include conf.d/api.conf;

    server {
        listen 80;
        server_name legacy.example.com;
    }
}
```

Create `internal/config/testdata/combine/conf.d/api.conf`:

```nginx
server {
    listen 443 ssl;
    server_name api.example.com;
}
```

- [ ] **Step 2: Write the test that fails**

Create `internal/config/combine_test.go`:

```go
package config_test

import (
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func parseCombine(t *testing.T) *config.Tree {
	t.Helper()
	tree, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "combine", "nginx.conf"),
	})
	require.NoError(t, err)
	return tree
}

func TestParseWithoutCombineKeepsFilesSeparate(t *testing.T) {
	tree := parseCombine(t)

	require.Len(t, tree.Files, 2, "nginx.conf e conf.d/api.conf")
}

func TestCombineProducesASingleFile(t *testing.T) {
	combined, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	require.Len(t, combined.Files, 1)
}

func TestCombineReplacesIncludeWithIncludedNodes(t *testing.T) {
	combined, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var http *config.Node
	combined.Walk(func(n *config.Node) bool {
		if n.Directive == "http" {
			http = n
			return false
		}
		return true
	})
	require.NotNil(t, http)

	var names []string
	for _, child := range http.Block {
		names = append(names, child.Directive)
	}
	require.Equal(t, []string{"server", "server"}, names,
		"the include is gone and became the server of the included file")
}

// Origin is what lets the agent know which real file to edit after seeing the
// resolved configuration.
func TestCombineFillsOriginWithRealFile(t *testing.T) {
	combined, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var api *config.Node
	combined.Walk(func(n *config.Node) bool {
		if n.Directive == "server_name" && len(n.Args) > 0 && n.Args[0] == "api.example.com" {
			api = n
			return false
		}
		return true
	})
	require.NotNil(t, api)

	require.NotNil(t, api.Origin)
	require.Contains(t, api.Origin.File, "api.conf")
	require.Greater(t, api.Origin.Line, 0)
}

func TestCombineKeepsOriginOfMainFile(t *testing.T) {
	combined, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var legacy *config.Node
	combined.Walk(func(n *config.Node) bool {
		if n.Directive == "server_name" && len(n.Args) > 0 && n.Args[0] == "legacy.example.com" {
			legacy = n
			return false
		}
		return true
	})
	require.NotNil(t, legacy)

	require.NotNil(t, legacy.Origin)
	require.Contains(t, legacy.Origin.File, "nginx.conf")
}

// The IDs of the combined tree are renumbered over the resolved structure:
// that is the structure the agent sees and operates on.
func TestCombineRenumbersIDsOverResolvedStructure(t *testing.T) {
	combined, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	api := config.FindByID(combined, "h.s0")
	require.NotNil(t, api)
	require.Equal(t, "server", api.Directive)
	require.Contains(t, api.Origin.File, "api.conf",
		"the first server of the resolved tree comes from the include")

	legacy := config.FindByID(combined, "h.s1")
	require.NotNil(t, legacy)
	require.Contains(t, legacy.Origin.File, "nginx.conf")
}

// The hash of the combined tree differs from that of the non-combined one: they are views
// different, and confusing them would invalidate IDs for no reason.
func TestCombineRecomputesTheHash(t *testing.T) {
	original := parseCombine(t)
	combined, err := config.Combine(original)
	require.NoError(t, err)

	require.NotEmpty(t, combined.Hash)
	require.NotEqual(t, original.Hash, combined.Hash)
}
```

- [ ] **Step 3: Run the test to verify that it fails**

Run: `go test ./internal/config/ -run Combine -v`
Expected: FAIL — `undefined: config.Combine`.

- [ ] **Step 4: Write the minimum implementation**

Create `internal/config/combine.go`:

```go
package config

import "fmt"

// Combine resolves the includes, returning a single-file tree where
// every node carries its real origin.
//
// Resolution happens over our own tree, not through crossplane's
// CombineConfigs, because combining earlier would destroy the spans: they
// point at offsets of specific files. Here the original nodes stay intact and
// only the structure is reorganized.
func Combine(t *Tree) (*Tree, error) {
	if len(t.Files) == 0 {
		return &Tree{}, nil
	}

	main := t.Files[0]
	c := &combiner{files: t.Files, visited: map[string]bool{}}

	nodes, err := c.resolve(main)
	if err != nil {
		return nil, err
	}

	combined := &Tree{
		Files: []*File{{
			Path:   main.Path,
			Source: main.Source,
			Nodes:  nodes,
		}},
	}
	AssignIDs(combined.Files[0].Nodes, "")
	combined.Hash = Hash(combined)
	return combined, nil
}

// files is a slice, not a map, on purpose: an include with a glob can match
// several files, and iterating a map would give a different order on every
// run — which would change the IDs and the hash without the configuration
// changing.
type combiner struct {
	files   []*File
	visited map[string]bool
}

func (c *combiner) resolve(f *File) ([]*Node, error) {
	if c.visited[f.Path] {
		return nil, fmt.Errorf("circular include detected in %s", f.Path)
	}
	c.visited[f.Path] = true
	defer delete(c.visited, f.Path)

	return c.expand(f.Nodes)
}

func (c *combiner) expand(nodes []*Node) ([]*Node, error) {
	var out []*Node

	for _, n := range nodes {
		if n.Directive == "include" {
			included, err := c.expandInclude(n)
			if err != nil {
				return nil, err
			}
			out = append(out, included...)
			continue
		}

		clone := *n
		clone.Origin = &Origin{File: n.File, Line: n.Line}

		if len(n.Block) > 0 {
			children, err := c.expand(n.Block)
			if err != nil {
				return nil, err
			}
			clone.Block = children
		}
		out = append(out, &clone)
	}

	return out, nil
}

// expandInclude finds files that match the include pattern.
// Crossplane has already resolved the globs and returned each matched file as
// its own config, so it is enough to find the ones that match.
func (c *combiner) expandInclude(n *Node) ([]*Node, error) {
	var out []*Node

	for _, target := range c.filesForInclude(n) {
		nodes, err := c.resolve(target)
		if err != nil {
			return nil, err
		}
		out = append(out, nodes...)
	}

	return out, nil
}

// The iteration goes over the file slice, in the order crossplane returned
// them, so the result is deterministic.
func (c *combiner) filesForInclude(n *Node) []*File {
	var found []*File
	for _, f := range c.files {
		for _, arg := range n.Args {
			if matchInclude(f.Path, arg, n.File) {
				found = append(found, f)
				break
			}
		}
	}
	return found
}
```

Also create, in the same file, the path matching:

```go
// matchInclude decides whether a parsed file corresponds to the pattern of an
// include. The pattern may be relative to the file that declared it.
func matchInclude(path, pattern, declaredIn string) bool {
	if path == pattern {
		return true
	}
	base := filepath.Dir(declaredIn)
	if ok, _ := filepath.Match(filepath.Join(base, pattern), path); ok {
		return true
	}
	if ok, _ := filepath.Match(pattern, path); ok {
		return true
	}
	return false
}
```

Add `"path/filepath"` to imports.

- [ ] **Step 5: Run the tests to check that they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — 7 combine tests plus all of the above.

> `TestCombineRenumbersIDsOverResolvedStructure` requires that the `server` of the included file comes **before** the `server` declared in `nginx.conf`, because `include` appears before. If it fails, the problem is the order in `expand`, not the test.

- [ ] **Step 6: Commit**

```bash
git add internal/config/combine.go internal/config/combine_test.go internal/config/testdata/combine/
git commit -m "feat(config): include resolution with origin tracking"
```

---

### Task 13: `inspect` command

**Files:**
- Create: `internal/cli/inspect.go`
- Modify: `internal/cli/root.go` — register the command
- Test: `internal/cli/inspect_test.go`, `internal/cli/testdata/example.conf`

**Interfaces:**
- Consumes: `cli.Context`, `cli.NewRoot` (Task 6); `config.Parse`, `config.Combine`, `config.Tree` (Tasks 7–12); `output.New`, `output.Usage`, `output.Internal`, `output.RedactSet`, `output.RedactedValue` (Tasks 1–4)
- Produces: `cli.InspectData` (`Config []*config.File`, `Summary cli.Summary`); `cli.Summary` (`Files`, `Servers`, `Locations`, `Upstreams int`); method `(InspectData).Redacted(output.RedactSet) any`

- [ ] **Step 1: Create the fixture**

Create `internal/cli/testdata/example.conf`:

```nginx
events {}

http {
    server {
        listen 443 ssl;
        server_name api.example.com;
        ssl_certificate_key /etc/ssl/private/api.key;

        location / {
            proxy_pass http://backend;
        }

        location /health {
            access_log off;
        }
    }

    upstream backend {
        server 10.0.0.1:8080;
    }
}
```

- [ ] **Step 2: Write the test that fails**

Create `internal/cli/inspect_test.go`:

```go
package cli_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/cli"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

func runInspect(t *testing.T, args ...string) (output.ExitCode, *output.Envelope, string) {
	t.Helper()
	var out, errBuf bytes.Buffer

	code := cli.Execute(args, &out, &errBuf, false)

	var env output.Envelope
	if out.Len() > 0 {
		require.NoError(t, json.Unmarshal(out.Bytes(), &env), "output: %s", out.String())
	}
	return code, &env, out.String()
}

func fixture(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "example.conf")
}

func TestInspectReturnsSuccess(t *testing.T) {
	code, env, _ := runInspect(t, "inspect", "-c", fixture(t))

	require.Equal(t, output.ExitOK, code)
	require.True(t, env.OK)
	require.Equal(t, "inspect", env.Command)
}

// The hash in the meta is the anchor of the IDs that come out in the data.
func TestInspectPublishesConfigHashInMeta(t *testing.T) {
	_, env, _ := runInspect(t, "inspect", "-c", fixture(t))

	require.NotEmpty(t, env.Meta.ConfigHash)
	require.Contains(t, env.Meta.ConfigHash, "sha256:")
}

func TestInspectSummarizesTheConfiguration(t *testing.T) {
	_, _, raw := runInspect(t, "inspect", "-c", fixture(t))

	var response struct {
		Data struct {
			Summary cli.Summary `json:"summary"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))

	require.Equal(t, 1, response.Data.Summary.Servers)
	require.Equal(t, 2, response.Data.Summary.Locations)
	require.Equal(t, 1, response.Data.Summary.Upstreams)
	require.Equal(t, 1, response.Data.Summary.Files)
}

// The IDs have to be in the JSON: it is through them that the agent references
// a node in the next call.
func TestInspectEmitsIDsInTheTree(t *testing.T) {
	_, _, raw := runInspect(t, "inspect", "-c", fixture(t))

	require.Contains(t, raw, `"id":"h.s0"`)
	require.Contains(t, raw, `"id":"h.s0.l0"`)
	require.Contains(t, raw, `"id":"h.u0"`)
}

func TestInspectEmitsSpans(t *testing.T) {
	_, _, raw := runInspect(t, "inspect", "-c", fixture(t))

	require.Contains(t, raw, `"span"`)
	require.Contains(t, raw, `"head_span"`)
}

// The test that closes the redaction cycle: the sensitive value must not show
// up in the output, but the directive must.
func TestInspectRedactsPrivateKey(t *testing.T) {
	_, _, raw := runInspect(t, "inspect", "-c", fixture(t))

	require.NotContains(t, raw, "/etc/ssl/private/api.key")
	require.Contains(t, raw, "ssl_certificate_key", "the directive stays visible")
	require.Contains(t, raw, output.RedactedValue)
}

// A missing file is an IO failure, not a usage error: the flag was correct;
// it was the disk that did not have the file.
func TestInspectWithMissingFileIsInternalFailure(t *testing.T) {
	code, env, _ := runInspect(t, "inspect", "-c", "testdata/does-not-exist.conf")

	require.Equal(t, output.ExitInternal, code)
	require.False(t, env.OK)
}

func TestInspectWithoutAnyConfigIsUsageError(t *testing.T) {
	code, env, _ := runInspect(t, "inspect")

	require.Equal(t, output.ExitUsage, code)
	require.False(t, env.OK)
}

func TestInspectCombineResolveIncludes(t *testing.T) {
	code, _, raw := runInspect(t, "inspect", "--combine",
		"-c", filepath.Join("..", "config", "testdata", "combine", "nginx.conf"))

	require.Equal(t, output.ExitOK, code)
	require.Contains(t, raw, `"origin"`)
	require.NotContains(t, raw, `"directive":"include"`,
		"the include was resolved and no longer shows up in the tree")
}
```

- [ ] **Step 3: Run the test to verify that it fails**

Run: `go test ./internal/cli/ -run Inspect -v`
Expected: FAIL — unknown `inspect` command.

- [ ] **Step 4: Write the minimum implementation**

Create `internal/cli/inspect.go`:

```go
package cli

import (
	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/spf13/cobra"
)

// Summary is a one-line view of the configuration. It exists for the agent to know
// the size of what you are looking at without having to tell us.
type Summary struct {
	Files     int `json:"files"`
	Servers   int `json:"servers"`
	Locations int `json:"locations"`
	Upstreams int `json:"upstreams"`
}

// InspectData is the full dump: tree plus summary.
type InspectData struct {
	Config  []*config.File `json:"config"`
	Summary Summary        `json:"summary"`
}

// Redacted returns a copy with the sensitive values replaced. The copy is deep
// on the affected nodes: the original tree is never changed, otherwise a later
// fmt would write *** into the user's file.
func (d InspectData) Redacted(rs output.RedactSet) any {
	if rs.Empty() {
		return d
	}

	files := make([]*config.File, 0, len(d.Config))
	for _, f := range d.Config {
		files = append(files, &config.File{
			Path:   f.Path,
			Source: f.Source,
			Nodes:  redactNodes(f.Nodes, rs),
		})
	}
	return InspectData{Config: files, Summary: d.Summary}
}

func redactNodes(nodes []*config.Node, rs output.RedactSet) []*config.Node {
	out := make([]*config.Node, 0, len(nodes))
	for _, n := range nodes {
		clone := *n
		if rs.Matches(n.Directive, n.Args) {
			clone.Args = []string{output.RedactedValue}
		}
		if len(n.Block) > 0 {
			clone.Block = redactNodes(n.Block, rs)
		}
		out = append(out, &clone)
	}
	return out
}

func newInspectCmd(ctx *Context) *cobra.Command {
	var combine bool

	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Full dump: configuration tree and summary",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			path := configPath(ctx)
			if path == "" {
				return output.Usage("give the configuration with -c or in nginx.config")
			}

			tree, err := config.Parse(config.ParseOptions{Path: path})
			if err != nil {
				return output.Internal(err, "%s", err.Error())
			}

			if combine {
				tree, err = config.Combine(tree)
				if err != nil {
					return output.Internal(err, "%s", err.Error())
				}
			}

			env := output.New("inspect")
			env.Data = InspectData{Config: tree.Files, Summary: summarize(tree)}
			env.Meta.ConfigHash = tree.Hash
			return ctx.Renderer.Render(env)
		},
	}

	cmd.Flags().BoolVar(&combine, "combine", false, "resolve includes into a single tree")
	return cmd
}

func configPath(ctx *Context) string {
	if ctx.Flags.ConfigPath != "" {
		return ctx.Flags.ConfigPath
	}
	if ctx.Settings != nil {
		return ctx.Settings.Nginx.Config
	}
	return ""
}

func summarize(t *config.Tree) Summary {
	s := Summary{Files: len(t.Files)}
	t.Walk(func(n *config.Node) bool {
		switch n.Directive {
		case "server":
			s.Servers++
		case "location":
			s.Locations++
		case "upstream":
			s.Upstreams++
		}
		return true
	})
	return s
}
```

In `internal/cli/root.go`, register the command with `version`:

```go
	root.AddCommand(newVersionCmd(ctx))
	root.AddCommand(newInspectCmd(ctx))
```

- [ ] **Step 5: Run the tests to check that they pass**

Run: `go test ./... -v`
Expected: PASS on all packages.

> `TestInspectSummarizesTheConfiguration` counts `server` as directive. The fixture has `server 10.0.0.1:8080;` **inside** the upstream, which is also called `server`. If the test counts 2 servers, the fix is to only count `server` that opens block (`n.HasBlock()`), which is also the correct behavior — `server` inside `upstream` is another directive. Apply the fix; do not change the test.

- [ ] **Step 6: Check by hand**

```bash
go build -o /tmp/ngx ./cmd/ngx
/tmp/ngx inspect -c internal/cli/testdata/example.conf | head -c 400; echo
/tmp/ngx inspect -c internal/cli/testdata/example.conf | grep -c 'private/api.key'
```

Expected: a JSON envelope with the tree; `grep -c` prints `0`, confirming that the private key was not leaked.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/ 
git commit -m "feat(cli): inspect command"
```

---

### Task 14: README, vet and final plan check

**Files:**
- Create: `README.md`, `Makefile`
- Test: none new; runs the entire suite

- Produces: `make test`, `make fuzz`, `make build`**Interfaces:**
- Consumption: everything

- [ ] **Step 1: Create the Makefile**

```makefile
.PHONY: build test vet fuzz clean

build:
	go build -o bin/ngx ./cmd/ngx

test:
	go test ./... -race

vet:
	go vet ./...

fuzz:
	go test ./internal/config/ -run FuzzTokenizeSpans -fuzz FuzzTokenizeSpans -fuzztime 60s
	go test ./internal/config/ -run FuzzAlignment -fuzz FuzzAlignment -fuzztime 60s

clean:
	rm -rf bin/
```

- [ ] **Step 2: Create the README**

Create `README.md`:

````markdown
# ngx

A Go CLI that makes nginx safely operable by AI agents.

Today an agent that has to touch nginx reads `.conf` as loose text, edits it with
string replacement, and finds out it got it wrong when `nginx -t` fails — or worse,
when the reload takes production down. `ngx` unifies parsing, analysis and
mutation in a single binary, with structured JSON output by default, selector-
based reading instead of dumping thousands of lines, and transactional changes
with automatic rollback.

## Status

**v0.1, under construction.** Read-only: no command changes the configuration of
a running server. Transactional mutation arrives in v0.2.

Working today:

- `ngx inspect` — the full configuration tree, with stable IDs, byte offsets
  and a summary
- `ngx version`

## Example

```console
$ ngx inspect -c /etc/nginx/nginx.conf | jq '.data.summary'
{
  "files": 4,
  "servers": 12,
  "locations": 37,
  "upstreams": 5
}
```

Sensitive values — private keys, authorization headers — are redacted by
default before leaving. `--no-redact` is only accepted when the output is a
terminal.

## Building

```console
$ make fuzz # tokenizer and alignment fuzzers$ make build # compiles to bin/ngx
$ make test # complete suite with race detector
```

Requires Go 1.25. `.tool-versions` pins the version for asdf users.

## Design

The architecture decisions and the reasoning behind each are in
[`docs/superpowers/specs/`](docs/superpowers/specs/).

## License

MIT. Copyright (c) 2026 Eduardo Benck.
````

- [ ] **Step 3: Run the complete suite with race detector**

Run: `make vet && make test`
Expected: PASS on all packages, without vet warnings.

- [ ] **Step 4: Run the fuzzers**

Run: `make fuzz`
Expected: no failures. New cases in `testdata/fuzz/` must be committed as regressions.

- [ ] **Step 5: Commit**

```bash
git add README.md Makefile
git commit -m "chore: makefile and readme"
```

---

## Spec coverage check

| Spec section | Task |
|---|---|
| §3 architecture, layer rule | 1, 4, 6 |
| §4.1 `Node` model, two spans | 7, 9 |
| §4.2 IDs among siblings of the same type | 10 |
| §4.3 canonical hash | 11 |
| §5 R1-R4 selectors | **Plan 2** |
| §6.1 envelope | 1 |
| §6.2 exit codes | 2 |
| §6.3 redaction, `--no-redact` on a TTY | 3, 4, 13 |
| §7 runtime, drift | **Plan 3** |
| §8 `inspect` | 13 |
| §8 `get`, `tree` | **Plan 2** |
| §8 `status`, `fmt`, `test`, `diff` | **Plan 3** |
| §8.1 configuration file | 5 |
| §9 property test for spans | 8, 9 |
| §9 fuzzing | 8, 9 |
| §9 golden files, fake nginx, integration | **Plan 3** |
| §10 repository, license | 1, 14 |
| §10 CI and goreleaser | **Plan 3** |

**Refinement of §9 of the spec:** the spec describes the span property as "reconstituting the file byte by byte". The concrete, verifiable formulation adopted here is stronger and lives in `TestRootSpansCoverEverySignificantByte` plus `TestChildSpansAreContainedInParent`: every non-white byte belongs to the span of some root node, spans of children are contained in those of parents, and siblings do not overlap. It is worth updating the spec to this wording when Plan 1 closes.
