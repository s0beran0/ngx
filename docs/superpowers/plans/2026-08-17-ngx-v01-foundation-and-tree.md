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
func TestEnvelopeSerializaDiagnosticsVaziosComoArray(t *testing.T) {
	env := output.New("status")

	b, err := json.Marshal(env)
	require.NoError(t, err)

	require.Contains(t, string(b), `"diagnostics":[]`)
	require.NotContains(t, string(b), `"diagnostics":null`)
}

func TestEnvelopeNasceOK(t *testing.T) {
	env := output.New("status")

	require.True(t, env.OK)
	require.Equal(t, "status", env.Command)
	require.NotEmpty(t, env.NgxVersion)
}

// The error severity is what knocks the ok out of the envelope. Warning and info no.
func TestAddDiagnosticErrorDerrubaOK(t *testing.T) {
	env := output.New("test")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityWarning, Message: "cuidado"})
	require.True(t, env.OK, "a warning must not bring ok down")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityError, Message: "falhou"})
	require.False(t, env.OK, "error deve derrubar ok")

	require.Len(t, env.Diagnostics, 2)
}

// Missing optional fields should not pollute the agent's output.
func TestDiagnosticOmiteCamposVazios(t *testing.T) {
	b, err := json.Marshal(output.Diagnostic{
		Severity: output.SeverityError,
		Code:     "NGX-0002",
		Message:  "seletor malformado",
	})
	require.NoError(t, err)

	s := string(b)
	require.False(t, strings.Contains(s, `"file"`), "file vazio deve ser omitido")
	require.False(t, strings.Contains(s, `"line"`), "line zero deve ser omitido")
	require.False(t, strings.Contains(s, `"selector"`), "selector vazio deve ser omitido")
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

// Severity classifica um diagnostico. Apenas SeverityError derruba o ok
// of the envelope.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Diagnosis is a localized finding. The selector and id fields exist to
// that the agent acts on the finding without reviewing the configuration.
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

// Meta carrega dados sobre a execucao. ConfigHash ancora os IDs devolvidos
// in this answer: an ID is only valid against the hash that came with it.
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

// AddDiagnostic anexa um diagnostico, derrubando ok se for um erro.
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
git commit -m "feat(output): envelope de saida e diagnosticos"
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

func TestCodeOfNilEhSucesso(t *testing.T) {
	require.Equal(t, output.ExitOK, output.CodeOf(nil))
}

// An error that does not load code is an internal error, not a success.
func TestCodeOfErroDesconhecidoEhInterno(t *testing.T) {
	require.Equal(t, output.ExitInternal, output.CodeOf(errors.New("boom")))
}

func TestConstrutoresCarregamSeuCodigo(t *testing.T) {
	casos := []struct {
		nome string
		err  error
		want output.ExitCode
	}{
		{"usage", output.Usage("seletor malformado: %q", "http..server"), output.ExitUsage},
		{"config invalida", output.InvalidConfig("nginx -t falhou"), output.ExitInvalidConfig},
		{"drift", output.Drift("config em disco mudou apos o ultimo reload"), output.ExitDrift},
		{"hash", output.HashMismatch("sha256:aa", "sha256:bb"), output.ExitHashMismatch},
		{"interno", output.Internal(errors.New("io"), "falha ao ler"), output.ExitInternal},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			require.Equal(t, c.want, output.CodeOf(c.err))
		})
	}
}

// The code needs to survive wrapping, otherwise it is an error wrapped in a
// camada intermediaria vira exit 1 silenciosamente.
func TestCodeOfAtravessaWrapping(t *testing.T) {
	err := fmt.Errorf("ao carregar configuracao: %w", output.Usage("flag invalida"))

	require.Equal(t, output.ExitUsage, output.CodeOf(err))
}

// Todo erro tipado precisa render um diagnostico exibivel ao agente.
func TestErroExpoeDiagnostico(t *testing.T) {
	err := output.Usage("seletor malformado: %q", "http..server")

	var e *output.Error
	require.True(t, errors.As(err, &e))
	require.Equal(t, output.SeverityError, e.Diag.Severity)
	require.Equal(t, "NGX-0002", e.Diag.Code)
	require.Contains(t, e.Diag.Message, "http..server")
}

// HashMismatch is the error that prevents the agent from acting on an aged ID.
// The message needs to show both hashes so he knows what happened.
func TestHashMismatchMostraOsDoisHashes(t *testing.T) {
	err := output.HashMismatch("sha256:esperado", "sha256:atual")

	require.Contains(t, err.Error(), "sha256:esperado")
	require.Contains(t, err.Error(), "sha256:atual")
}
```

- [ ] **Step 2: Run the test to verify that it fails**

Run: `go test ./internal/output/ -run "TestCodeOf|TestConstructors|TestErro|TestHashMismatch" -v`
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
// abaixo; 4 (lint), 5 e 6 (apply) e 8 (mutacao ambigua) pertencem a comandos
// that do not yet exist and are not documented as supported until they are
// emitiveis.
type ExitCode int

const (
	ExitOK            ExitCode = 0
	ExitInternal      ExitCode = 1
	ExitUsage         ExitCode = 2
	ExitInvalidConfig ExitCode = 3
	ExitDrift         ExitCode = 7
	ExitHashMismatch  ExitCode = 9
)

// Error is an error that carries its own exit code and diagnoses it
// correspondente. Comandos nunca escolhem exit code diretamente: eles
// devolvem um destes, e main.go traduz num unico ponto.
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

// Usage sinaliza erro de uso: flag invalida, seletor malformado, argumento
// obrigatorio ausente.
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
func HashMismatch(esperado, atual string) *Error {
	return newError(ExitHashMismatch, "NGX-0009",
		"a configuracao mudou desde a leitura: esperado %s, atual %s", esperado, atual)
}

// Internal involves an IO failure or a defect in ngx itself.
func Internal(err error, format string, args ...any) *Error {
	e := newError(ExitInternal, "NGX-0001", format, args...)
	e.Err = err
	return e
}

// Code Of extracts the exit code from an iron, going through wrapping. An error without
// codigo e tratado como falha interna, nunca como sucesso.
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
git commit -m "feat(output): erros tipados carregando exit code"
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

// The spec uses three formats for the same thing. Everyone needs to function as
// written, so that a configuration copied from the spec does not fail silently.
func TestParseRedactRuleAceitaOsTresFormatosDaSpec(t *testing.T) {
	casos := []struct {
		entrada     string
		wantDir     string
		wantArgPref []string
	}{
		{"ssl_certificate_key", "ssl_certificate_key", nil},
		{"proxy_set_header Authorization", "proxy_set_header", []string{"Authorization"}},
		{"**.auth_basic_user_file", "auth_basic_user_file", nil},
	}

	for _, c := range casos {
		t.Run(c.entrada, func(t *testing.T) {
			r, err := output.ParseRedactRule(c.entrada)
			require.NoError(t, err)
			require.Equal(t, c.wantDir, r.Directive)
			require.Equal(t, c.wantArgPref, r.ArgPrefix)
		})
	}
}

func TestParseRedactRuleRejeitaEntradaVazia(t *testing.T) {
	_, err := output.ParseRedactRule("   ")
	require.Error(t, err)
}

func TestRuleCasaPorNomeDeDiretiva(t *testing.T) {
	r, err := output.ParseRedactRule("ssl_certificate_key")
	require.NoError(t, err)

	require.True(t, r.Matches("ssl_certificate_key", []string{"/etc/ssl/priv.key"}))
	require.False(t, r.Matches("ssl_certificate", []string{"/etc/ssl/pub.crt"}))
}

func TestRuleComPrefixoDeArgsExigeOsArgs(t *testing.T) {
	r, err := output.ParseRedactRule("proxy_set_header Authorization")
	require.NoError(t, err)

	require.True(t, r.Matches("proxy_set_header", []string{"Authorization", "Bearer xyz"}))
	require.False(t, r.Matches("proxy_set_header", []string{"Host", "$host"}),
		"another header must not be redacted")
	require.False(t, r.Matches("proxy_set_header", nil),
		"with no args it cannot match a rule that requires a prefix")
}

func TestRedactSetCasaQualquerRegra(t *testing.T) {
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

func TestRedactSetVazioNaoCasaNada(t *testing.T) {
	set, err := output.NewRedactSet(nil)
	require.NoError(t, err)

	require.True(t, set.Empty())
	require.False(t, set.Matches("ssl_certificate_key", []string{"/k.pem"}))
}

func TestNewRedactSetPropagaRegraInvalida(t *testing.T) {
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

// Redactable and implemented by any data that knows how to produce a copy
// written by yourself. Writing happens in serialization, never in the tree
// in memory: if the tree were written in parse, fmt would write *** inside
// from the user's .conf.
type Redactable interface {
	Redacted(rs RedactSet) any
}

// RedactRule matches a directive by name, optionally requiring a prefix
// de argumentos.
type RedactRule struct {
	Directive string
	ArgPrefix []string
}

// ParseRedactRule reads an input from output.redact. Accepts all three formats
// that the spec uses: directive name, name prefixed with arguments, and the
// context prefix "**." — which is redundant, because rules already apply in
// any context, but it is accepted to not break configurations written to
// from the spec.
func ParseRedactRule(s string) (RedactRule, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "**.")

	campos := strings.Fields(s)
	if len(campos) == 0 {
		return RedactRule{}, fmt.Errorf("regra de redacao vazia")
	}

	r := RedactRule{Directive: campos[0]}
	if len(campos) > 1 {
		r.ArgPrefix = campos[1:]
	}
	return r, nil
}

// Matches informa se a diretiva dada deve ter seu valor redigido.
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

// RedactSet and the active rule set.
type RedactSet struct {
	rules []RedactRule
}

// NewRedactSet compila as entradas de output.redact.
func NewRedactSet(entradas []string) (RedactSet, error) {
	var set RedactSet
	for _, e := range entradas {
		r, err := ParseRedactRule(e)
		if err != nil {
			return RedactSet{}, fmt.Errorf("regra de redacao %q: %w", e, err)
		}
		set.rules = append(set.rules, r)
	}
	return set, nil
}

// Empty informa se nenhuma regra esta ativa.
func (s RedactSet) Empty() bool { return len(s.rules) == 0 }

// Matches informa se alguma regra casa a diretiva dada.
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
git commit -m "feat(output): redacao de valores sensiveis"
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

type dadoRedigivel struct {
	Valor string `json:"valor"`
}

func (d dadoRedigivel) Redacted(rs output.RedactSet) any {
	if rs.Matches("ssl_certificate_key", []string{d.Valor}) {
		return dadoRedigivel{Valor: output.RedactedValue}
	}
	return d
}

type dadoHumano struct{}

func (dadoHumano) RenderHuman(w io.Writer) error {
	_, err := io.WriteString(w, "saida humana\n")
	return err
}

// Auto format without TTY has to become JSON: what about the case of the agent reading a pipe.
func TestFormatAutoSemTTYProduzJSON(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatAuto, IsTTY: false}

	require.NoError(t, r.Render(output.New("status")))

	var env output.Envelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, "status", env.Command)
}

// With TTY and no human renderer in the data, it falls to indented JSON instead of
// print the raw Go struct.
func TestFormatAutoComTTYUsaRenderHumanQuandoDisponivel(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatAuto, IsTTY: true}

	env := output.New("status")
	env.Data = dadoHumano{}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "saida humana")
}

// The gate that the newsroom exists to close: a human at the terminal can see the
// secret, an agent reading the pipe cannot even ask for it.
func TestNoRedactEhRecusadoSemTTY(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, NoRedact: true}

	err := r.Render(output.New("get"))

	require.Error(t, err)
	require.Equal(t, output.ExitUsage, output.CodeOf(err))
}

func TestNoRedactEhAceitoComTTY(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{
		Out: &buf, Format: output.FormatJSON, IsTTY: true,
		Redact: set, NoRedact: true,
	}

	env := output.New("get")
	env.Data = dadoRedigivel{Valor: "/etc/ssl/priv.key"}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "/etc/ssl/priv.key")
}

// Without --no-redact, the data goes through redacting before being serialized.
func TestRenderAplicaRedacaoNoDado(t *testing.T) {
	var buf bytes.Buffer
	set, err := output.NewRedactSet([]string{"ssl_certificate_key"})
	require.NoError(t, err)

	r := &output.Renderer{Out: &buf, Format: output.FormatJSON, IsTTY: false, Redact: set}

	env := output.New("get")
	env.Data = dadoRedigivel{Valor: "/etc/ssl/priv.key"}
	require.NoError(t, r.Render(env))

	require.NotContains(t, buf.String(), "/etc/ssl/priv.key")
	require.Contains(t, buf.String(), output.RedactedValue)
}

// Quiet suprime a saida de sucesso mas nunca a de erro: um agente precisa
// know what went wrong.
func TestQuietSuprimeSucessoMasNaoErro(t *testing.T) {
	var sucesso bytes.Buffer
	r := &output.Renderer{Out: &sucesso, Format: output.FormatJSON, Quiet: true}
	require.NoError(t, r.Render(output.New("status")))
	require.Empty(t, sucesso.String())

	var falha bytes.Buffer
	r2 := &output.Renderer{Out: &falha, Format: output.FormatJSON, Quiet: true}
	env := output.New("test")
	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityError, Message: "falhou"})
	require.NoError(t, r2.Render(env))
	require.Contains(t, falha.String(), "falhou")
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

// Format seleciona o renderizador. FormatAuto decide por TTY.
type Format string

const (
	FormatAuto  Format = "auto"
	FormatJSON  Format = "json"
	FormatHuman Format = "human"
)

// HumanRenderable is implemented by data that knows how to present itself to a
// human. Data that does not implement falls to indented JSON, which is more
// Useful to print the raw Go struct.
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

// Render writes the envelope in resolved format.
func (r *Renderer) Render(env *Envelope) error {
	if r.NoRedact && !r.IsTTY {
		return Usage("--no-redact so e aceito quando a saida e um terminal")
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
		return Internal(err, "falha ao serializar a saida")
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
			return Internal(err, "falha ao escrever diagnostico")
		}
	}

	if hr, ok := env.Data.(HumanRenderable); ok {
		if err := hr.RenderHuman(r.Out); err != nil {
			return Internal(err, "falha ao renderizar saida humana")
		}
		return nil
	}

	if env.Data == nil {
		return nil
	}
	b, err := json.MarshalIndent(env.Data, "", "  ")
	if err != nil {
		return Internal(err, "falha ao serializar a saida")
	}
	if _, err := fmt.Fprintln(r.Out, string(b)); err != nil {
		return Internal(err, "falha ao escrever saida")
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
git commit -m "feat(output): renderers json e humano com portao de --no-redact"
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

func escreve(t *testing.T, dir, nome, conteudo string) string {
	t.Helper()
	p := filepath.Join(dir, nome)
	require.NoError(t, os.WriteFile(p, []byte(conteudo), 0o644))
	return p
}

// Without any files, the defaults need to be usable on their own.
func TestLoadSemArquivosUsaDefaults(t *testing.T) {
	dir := t.TempDir()

	s, err := settings.Load(filepath.Join(dir, "global.yaml"), filepath.Join(dir, "local.yaml"))

	require.NoError(t, err)
	require.Equal(t, "auto", s.Output.Format)
	require.NotEmpty(t, s.Output.Redact, "a redacao vem ligada por padrao")
	require.Contains(t, s.Output.Redact, "ssl_certificate_key")
}

func TestLoadLeArquivoGlobal(t *testing.T) {
	dir := t.TempDir()
	global := escreve(t, dir, "global.yaml", `
nginx:
  binary: /usr/sbin/nginx
  config: /etc/nginx/nginx.conf
output:
  format: json
`)

	s, err := settings.Load(global, filepath.Join(dir, "ausente.yaml"))

	require.NoError(t, err)
	require.Equal(t, "/usr/sbin/nginx", s.Nginx.Binary)
	require.Equal(t, "json", s.Output.Format)
}

// The spec rule: the local overrides the global, key by key.
func TestLocalSobrescreveGlobal(t *testing.T) {
	dir := t.TempDir()
	global := escreve(t, dir, "global.yaml", `
nginx:
  binary: /usr/sbin/nginx
  config: /etc/nginx/nginx.conf
output:
  format: json
`)
	local := escreve(t, dir, "local.yaml", `
nginx:
  config: /tmp/teste/nginx.conf
`)

	s, err := settings.Load(global, local)

	require.NoError(t, err)
	require.Equal(t, "/tmp/teste/nginx.conf", s.Nginx.Config, "local vence")
	require.Equal(t, "/usr/sbin/nginx", s.Nginx.Binary, "a key that was not overridden survives")
	require.Equal(t, "json", s.Output.Format)
}

// A file written from the full spec contains version keys
// future ones. They need to be ignored, not become a mistake.
func TestChavesDeVersoesFuturasSaoIgnoradas(t *testing.T) {
	dir := t.TempDir()
	global := escreve(t, dir, "global.yaml", `
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

	s, err := settings.Load(global, filepath.Join(dir, "ausente.yaml"))

	require.NoError(t, err)
	require.Equal(t, "/usr/sbin/nginx", s.Nginx.Binary)
}

func TestYAMLInvalidoVirarErro(t *testing.T) {
	dir := t.TempDir()
	ruim := escreve(t, dir, "ruim.yaml", "nginx: [isto: nao: fecha")

	_, err := settings.Load(ruim, filepath.Join(dir, "ausente.yaml"))

	require.Error(t, err)
}

// Se o usuario declara redact, a lista dele substitui a default em vez de
// add — otherwise it cannot remove a standard rule.
func TestRedactDeclaradoSubstituiODefault(t *testing.T) {
	dir := t.TempDir()
	global := escreve(t, dir, "global.yaml", `
output:
  redact:
    - minha_diretiva_secreta
`)

	s, err := settings.Load(global, filepath.Join(dir, "ausente.yaml"))

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

// Output controla formato e redacao.
type Output struct {
	Format string   `koanf:"format"`
	Redact []string `koanf:"redact"`
}

// Settings is the effective configuration of ngx.
type Settings struct {
	Nginx  Nginx  `koanf:"nginx"`
	Output Output `koanf:"output"`
}

// Defaults returns the configuration used when no file exists. The
// wording is connected: without it, a get can leak private key path
// into the context of an LLM running on a third-party API.
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

// Load merges the global file with the local one, with the local winning key
// key. Missing file is not an error.
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
			return nil, fmt.Errorf("ao carregar %s: %w", p, err)
		}
	}

	s := Defaults()
	if err := k.Unmarshal("", s); err != nil {
		return nil, fmt.Errorf("configuracao invalida: %w", err)
	}
	return s, nil
}
```

- [ ] **Step 5: Run the tests to check that they pass**

Run: `go test ./internal/settings/ -v`
Expected: PASS — 6 tests.

> If `TestRedactDeclaradoSubstituiODefault` fails because the koanf concatenated the lists instead of replacing, the fix is to clear `s.Output.Redact` before `Unmarshal` when `k.Exists("output.redact")` is true, and only then restore the default if the key does not exist. Make this correction within this step; do not change the test.

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

func TestFlagInvalidaProduzExitDeUso(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--flag-que-nao-existe"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)
}

func TestComandoDesconhecidoProduzExitDeUso(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"comando-inexistente"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)
}

// --json e --human sao mutuamente exclusivos; pedir os dois e erro de uso,
// not a silent precedence.
func TestJSONEHumanJuntosSaoErroDeUso(t *testing.T) {
	var out, errBuf bytes.Buffer

	code := cli.Execute([]string{"--json", "--human", "version"}, &out, &errBuf, false)

	require.Equal(t, output.ExitUsage, code)
}

func TestVersionSaiNoEnvelopeJSONSemTTY(t *testing.T) {
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
func TestErroDeExecucaoSaiNoEnvelope(t *testing.T) {
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
// Package cli monta a arvore de comandos. Comandos produzem valores e erros
// typed; formatting and exit code are the responsibility of output.
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
	env := output.New(comandoDe(ctx))
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
		Short:         "Opera o nginx com saida estruturada e mudancas transacionais",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return preparar(ctx, cmd)
		},
	}

	f := ctx.Flags
	p := root.PersistentFlags()
	p.StringVarP(&f.ConfigPath, "config", "c", "", "configuracao principal do nginx")
	p.BoolVar(&f.JSON, "json", false, "forca saida JSON")
	p.BoolVar(&f.Human, "human", false, "forca saida legivel")
	p.BoolVarP(&f.Quiet, "quiet", "q", false, "so erros")
	p.BoolVar(&f.NoColor, "no-color", false, "desliga cores")
	p.StringVar(&f.NginxBin, "nginx-bin", "", "caminho do binario do nginx")
	p.StringVar(&f.NginxVersion, "nginx-version", "", "assume esta versao do nginx")
	p.DurationVar(&f.Timeout, "timeout", 30*time.Second, "timeout das operacoes")
	p.StringVar(&f.Profile, "profile", "", "profile of the ngx configuration file")
	p.BoolVar(&f.NoRedact, "no-redact", false, "mostra valores sensiveis (so em terminal)")

	root.AddCommand(newVersionCmd(ctx))
	return root
}

func preparar(ctx *Context, cmd *cobra.Command) error {
	f := ctx.Flags
	ctx.Command = cmd.Name()

	if f.JSON && f.Human {
		return output.Usage("--json e --human sao mutuamente exclusivos")
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

	ctx.Renderer.Format = resolverFormato(f, s)
	ctx.Renderer.Redact = set
	ctx.Renderer.NoRedact = f.NoRedact
	ctx.Renderer.Quiet = f.Quiet

	cmd.SilenceUsage = true
	return nil
}

func resolverFormato(f *GlobalFlags, s *settings.Settings) output.Format {
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
		Short: "Mostra a versao do ngx",
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

// commandFrom returns the name of the command that was executing, so that the
// error envelope identifies the operation that failed. Before the snake
// resolve the command — global flag invalidates, for example — there is no name, and the
// fallback is the binary itself.
func comandoDe(ctx *Context) string {
	if ctx == nil || ctx.Command == "" {
		return "ngx"
	}
	return ctx.Command
}
```

Create `cmd/ngx/main.go`:

```go
// Command ngx and the entry point. The only responsibility here is the wiring
// and the translation of the exit code.
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

- [ ] **Step 6: Check torque by hand**

```bash
go build -o /tmp/ngx ./cmd/ngx
/tmp/ngx version | head -1
/tmp/ngx --json --human version; echo "exit=$?"
```

Expected: the first line is a JSON envelope with `"command":"version"`; the second prints an envelope with `NGX-0002` and `exit=2`.

- [ ] **Step 7: Commit**

```bash
git add cmd/ internal/cli/ go.mod go.sum
git commit -m "feat(cli): comando raiz, flags globais e traducao de exit code"
```

---

### Task 7: The tree and parse via crossplane

- Test: `internal/config/parse_test.go`, `internal/config/testdata/simples.conf`**Files:**
- Create: `internal/config/node.go`, `internal/config/parse.go`

**Interfaces:**
- Consumes: `output.RedactSet`, `output.Redactable`, `output.RedactedValue` (Task 3)
- Produces: `config.Span` (`Start`, `End`), `config.Origin` (`File`, `Line`), `config.Node` (fields as per §4.1 of the spec), `config.File` (`Path string`, `Source []byte`, `Nodes []*Node`), `config.Tree` (`Files []*File`, `Hash string`), `config.ParseOptions` (`Path string`, `Open func(string) (io.ReadCloser, error)`), `config.Parse(ParseOptions) (*Tree, error)`, `(*Node).IsComment() bool`, `(*Node).HasBlock() bool`, `(*Tree).Walk(func(*Node) bool)`

- [ ] **Step 1: Install the crossplane and create the fixture**

```bash
go get github.com/nginxinc/nginx-go-crossplane@latest
```

Create `internal/config/testdata/simples.conf`:

```nginx
# configuracao de exemplo
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
        server_name api.exemplo.com;

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

func parseSimples(t *testing.T) *config.Tree {
	t.Helper()
	tree, err := config.Parse(config.ParseOptions{
		Path: filepath.Join("testdata", "simples.conf"),
	})
	require.NoError(t, err)
	return tree
}

func TestParseProduzUmArquivoComFonte(t *testing.T) {
	tree := parseSimples(t)

	require.Len(t, tree.Files, 1)
	require.NotEmpty(t, tree.Files[0].Source, "the original source has to be kept for the spans")
	require.Contains(t, tree.Files[0].Path, "simples.conf")
}

func TestParsePreservaComentarios(t *testing.T) {
	tree := parseSimples(t)

	var comentarios int
	tree.Walk(func(n *config.Node) bool {
		if n.IsComment() {
			comentarios++
			require.NotNil(t, n.Comment)
			require.Contains(t, *n.Comment, "configuracao de exemplo")
		}
		return true
	})

	require.Equal(t, 1, comentarios)
}

func TestParseMonstaBlocosAninhados(t *testing.T) {
	tree := parseSimples(t)

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

func TestParseGuardaArgumentosEArquivo(t *testing.T) {
	tree := parseSimples(t)

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
	require.Contains(t, listen.File, "simples.conf")
}

func TestParseArquivoInexistenteVirarErro(t *testing.T) {
	_, err := config.Parse(config.ParseOptions{Path: "testdata/nao-existe.conf"})

	require.Error(t, err)
}

// The redaction happens at the exit: the tree in memory maintains the real value, otherwise
// fmt would write *** into the user's .conf.
func TestArvoreEmMemoriaNaoEhRedigida(t *testing.T) {
	tree := parseSimples(t)

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
// semantic tree comes from nginx-go-crossplane, byte offsets come from
// tokenizador deste pacote, e as duas sao casadas por sequencia de tokens.
package config

// Span is a range of bytes in the source file, with unique End.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Len returns the size of the range in bytes.
func (s Span) Len() int { return s.End - s.Start }

// Origin records where a no came from after resolving include.
type Origin struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Node is a directive. Span covers the entire directive, including the block and the
// delimitador final; HeadSpan cobre apenas o nome e os argumentos. Ter os
// two is what makes the v0.2 edit a byte replacement rather than
// a re-render of the file.
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

	// temBloco distinguishes "server {}" from "server;". The Block field does not work
	// for this: an empty block and an empty slice, indistinguishable from nil
	// after serialization.
	temBloco bool
}

// IsComment informs whether the no represents a comment.
func (n *Node) IsComment() bool { return n.Directive == "#" }

// HasBlock informs whether no opens a block, including an empty one.
func (n *Node) HasBlock() bool { return n.temBloco }

// File is a configuration file with its original source preserved. THE
// font is necessary so that spans can be resolved into text.
type File struct {
	Path   string  `json:"file"`
	Source []byte  `json:"-"`
	Nodes  []*Node `json:"parsed"`
}

// Tree and the complete result of a parse.
type Tree struct {
	Files []*File `json:"config"`
	Hash  string  `json:"-"`
}

// Walk percorre a arvore em pre-ordem. Se fn devolver false, os filhos
// from that they are not skipped.
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

// ParseOptions controls the reading. Open exists to allow testing with
// filesystem in memory, without touching disk.
type ParseOptions struct {
	Path string
	Open func(path string) (io.ReadCloser, error)
}

func (o ParseOptions) abrir(path string) (io.ReadCloser, error) {
	if o.Open != nil {
		return o.Open(path)
	}
	return os.Open(path)
}

// Parse reads the configuration and returns the canonical tree. Each file is
// parseado separadamente, preservando sua fonte: a resolucao de include e
// a view built on this tree, not a previous concatenation, to
// that the spans continue to point to real offsets of real files.
func Parse(opts ParseOptions) (*Tree, error) {
	payload, err := crossplane.Parse(opts.Path, &crossplane.ParseOptions{
		ParseComments:             true,
		CombineConfigs:            false,
		SingleFile:                false,
		SkipDirectiveArgsCheck:    true,
		SkipDirectiveContextCheck: true,
		ErrorOnUnknownDirectives:  false,
		Open:                      opts.abrir,
	})
	if err != nil {
		return nil, fmt.Errorf("ao parsear %s: %w", opts.Path, err)
	}

	tree := &Tree{}
	for _, cfg := range payload.Config {
		src, err := lerFonte(opts, cfg.File)
		if err != nil {
			return nil, err
		}
		tree.Files = append(tree.Files, &File{
			Path:   cfg.File,
			Source: src,
			Nodes:  converterDirectives(cfg.Parsed, cfg.File),
		})
	}
	return tree, nil
}

func lerFonte(opts ParseOptions, path string) ([]byte, error) {
	rc, err := opts.abrir(path)
	if err != nil {
		return nil, fmt.Errorf("ao ler %s: %w", path, err)
	}
	defer rc.Close()

	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("ao ler %s: %w", path, err)
	}
	return b, nil
}

func converterDirectives(ds crossplane.Directives, file string) []*Node {
	nodes := make([]*Node, 0, len(ds))
	for _, d := range ds {
		n := &Node{
			Directive: d.Directive,
			Args:      d.Args,
			File:      file,
			Line:      d.Line,
			Comment:   d.Comment,
			temBloco:  d.Block != nil,
		}
		if n.Args == nil {
			n.Args = []string{}
		}
		if d.Block != nil {
			n.Block = converterDirectives(d.Block, file)
		}
		nodes = append(nodes, n)
	}
	return nodes
}
```

- [ ] **Step 6: Run the tests to check that they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — 6 tests.

> `hasBlock` comes from `d.Block != nil`. If the crossplane returns a non-nil empty slice for empty blocks and nil for simple directives, this is already correct. If you return nil for both, `TestParseMonstaBlocosAninhados` still passes (the fixture blocks are not empty), but Task 9 will correct the detection using the next token. Don't invent an empty block test here.

- [ ] **Step 7: Commit**

```bash
git add internal/config/ go.mod go.sum
git commit -m "feat(config): arvore canonica e parse via crossplane"
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
func TestTokenSpansApontamParaOTextoOriginal(t *testing.T) {
	src := []byte("server {\n    listen 443 ssl;\n}\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	for _, tok := range toks {
		require.Equal(t, tok.Raw, string(src[tok.Start:tok.End]),
			"token %q em [%d,%d)", tok.Value, tok.Start, tok.End)
	}
}

func TestTokenizeSeparaDelimitadores(t *testing.T) {
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

// Quotes hide; and { from the tokenizer. Getting this wrong breaks the alignment
// integer in the first add_header with a semicolon inside.
func TestAspasProtegemDelimitadores(t *testing.T) {
	src := []byte(`add_header X-A "b; c { d }";`)

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Len(t, toks, 4)
	require.Equal(t, "add_header", toks[0].Value)
	require.Equal(t, "X-A", toks[1].Value)
	require.Equal(t, "b; c { d }", toks[2].Value, "o valor vem sem as aspas")
	require.Equal(t, `"b; c { d }"`, toks[2].Raw, "o raw mantem as aspas")
	require.True(t, toks[2].Quoted)
	require.Equal(t, config.TokenSemicolon, toks[3].Kind)
}

func TestAspasSimplesTambemFuncionam(t *testing.T) {
	toks, err := config.Tokenize([]byte(`return 200 'ok; fim';`))
	require.NoError(t, err)

	require.Equal(t, "ok; fim", toks[2].Value)
	require.True(t, toks[2].Quoted)
}

func TestEscapeDentroDeAspas(t *testing.T) {
	toks, err := config.Tokenize([]byte(`msg "diz \"oi\"";`))
	require.NoError(t, err)

	require.Equal(t, `diz "oi"`, toks[1].Value)
}

func TestComentarioVaiAteOFimDaLinha(t *testing.T) {
	src := []byte("# um comentario; com ponto e virgula\nlisten 80;\n")

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	require.Equal(t, config.TokenComment, toks[0].Kind)
	require.Equal(t, "# um comentario; com ponto e virgula", toks[0].Raw)
	require.Equal(t, " um comentario; com ponto e virgula", toks[0].Value,
		"o valor do comentario vem sem o # inicial")
	require.Equal(t, "listen", toks[1].Value)
}

func TestLinhaEColunaSaoBaseUm(t *testing.T) {
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

func TestAspasNaoFechadasVirarErro(t *testing.T) {
	_, err := config.Tokenize([]byte(`msg "sem fim;`))

	require.Error(t, err)
}

// Coverage: every byte that is not white space belongs to some token.
func TestTokensCobremTodoByteSignificativo(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "simples.conf"))
	require.NoError(t, err)

	toks, err := config.Tokenize(src)
	require.NoError(t, err)

	coberto := make([]bool, len(src))
	prev := 0
	for _, tok := range toks {
		require.GreaterOrEqual(t, tok.Start, prev, "tokens fora de ordem")
		for i := tok.Start; i < tok.End; i++ {
			coberto[i] = true
		}
		prev = tok.End
	}

	for i, b := range src {
		if coberto[i] {
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

// The fuzz ensures that for any input the tokenizer accepts, the
// spans continue to point to the actual text and in ascending order. And the network
// which underpins the surgical edition of v0.2.
func FuzzTokenizeSpans(f *testing.F) {
	f.Add("server { listen 80; }")
	f.Add(`add_header X "a; b";`)
	f.Add("# comentario\nhttp { }")
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
				t.Fatalf("token comeca em %d, antes do fim anterior %d", tok.Start, prev)
			}
			if tok.End > len(s) || tok.Start > tok.End {
				t.Fatalf("invalid span [%d,%d) for a source of %d bytes", tok.Start, tok.End, len(s))
			}
			if got := s[tok.Start:tok.End]; got != tok.Raw {
				t.Fatalf("raw %q difere da fonte %q em [%d,%d)", tok.Raw, got, tok.Start, tok.End)
			}
			if tok.Line < 1 || tok.Column < 1 {
				t.Fatalf("linha/coluna base zero: %d:%d", tok.Line, tok.Column)
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

// TokenKind classifica um token.
type TokenKind int

const (
	// TokenWord e um nome de diretiva ou um argumento.
	TokenWord TokenKind = iota
	TokenSemicolon
	TokenBlockStart
	TokenBlockEnd
	TokenComment
)

// Token is a lexeme with its exact position in bytes. Value and content
// semantic (without quotes, without the # of the comment); Raw and the original text.
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

// Tokenize breaks the source into tokens with byte offsets. Does not interpret
// diretiva nenhuma: so precisa saber onde cada lexema comeca e termina,
// respeitando aspas, escapes e comentarios.
func Tokenize(src []byte) ([]Token, error) {
	t := &tokenizer{src: src, line: 1, col: 1}
	for {
		t.pularEspacos()
		if t.pos >= len(t.src) {
			return t.tokens, nil
		}
		if err := t.proximo(); err != nil {
			return nil, err
		}
	}
}

func (t *tokenizer) pularEspacos() {
	for t.pos < len(t.src) && ehEspaco(t.src[t.pos]) {
		t.avancar()
	}
}

func (t *tokenizer) avancar() {
	if t.src[t.pos] == '\n' {
		t.line++
		t.col = 1
	} else {
		t.col++
	}
	t.pos++
}

func (t *tokenizer) proximo() error {
	start, line, col := t.pos, t.line, t.col

	switch c := t.src[t.pos]; {
	case c == ';':
		t.avancar()
		t.emitir(TokenSemicolon, ";", start, line, col, false)
		return nil
	case c == '{':
		t.avancar()
		t.emitir(TokenBlockStart, "{", start, line, col, false)
		return nil
	case c == '}':
		t.avancar()
		t.emitir(TokenBlockEnd, "}", start, line, col, false)
		return nil
	case c == '#':
		for t.pos < len(t.src) && t.src[t.pos] != '\n' {
			t.avancar()
		}
		t.emitir(TokenComment, string(t.src[start+1:t.pos]), start, line, col, false)
		return nil
	case c == '"' || c == '\'':
		return t.lerAspas(c, start, line, col)
	default:
		return t.lerPalavra(start, line, col)
	}
}

func (t *tokenizer) lerAspas(aspa byte, start, line, col int) error {
	t.avancar() // consome a aspa de abertura

	var valor []byte
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		switch {
		case c == '\\' && t.pos+1 < len(t.src):
			t.avancar()
			valor = append(valor, t.src[t.pos])
			t.avancar()
		case c == aspa:
			t.avancar() // consome a aspa de fechamento
			t.emitir(TokenWord, string(valor), start, line, col, true)
			return nil
		default:
			valor = append(valor, c)
			t.avancar()
		}
	}
	return fmt.Errorf("quote %q opened on line %d was never closed", string(aspa), line)
}

func (t *tokenizer) lerPalavra(start, line, col int) error {
	var valor []byte
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		if ehEspaco(c) || c == ';' || c == '{' || c == '}' {
			break
		}
		if c == '\\' && t.pos+1 < len(t.src) {
			valor = append(valor, c)
			t.avancar()
			valor = append(valor, t.src[t.pos])
			t.avancar()
			continue
		}
		valor = append(valor, c)
		t.avancar()
	}
	t.emitir(TokenWord, string(valor), start, line, col, false)
	return nil
}

func (t *tokenizer) emitir(kind TokenKind, valor string, start, line, col int, quoted bool) {
	t.tokens = append(t.tokens, Token{
		Kind:   kind,
		Value:  valor,
		Raw:    string(t.src[start:t.pos]),
		Start:  start,
		End:    t.pos,
		Line:   line,
		Column: col,
		Quoted: quoted,
	})
}

func ehEspaco(c byte) bool {
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
git commit -m "feat(config): tokenizador com offsets de byte"
```

---

### Task 9: Token↔tree alignment

**Files:**
- Create: `internal/config/align.go`
- Modify: `internal/config/parse.go` — call the alignment at the end of `Parse`
- Test: `internal/config/align_test.go`

**Interfaces:**
- Consumptions: `config.Node`, `config.File`, `config.Tree`, `config.Span` (Task 7); `config.Token`, `config.Tokenize` (Task 8)
- Produces: `config.alinhar(f *File) error` (not exported; called by `Parse`). After Task 9, every `*Node` returned by `Parse` has `Span`, `HeadSpan`, `Line` and `Column` populated, and `HasBlock()` reflects the actual presence of `{`.

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

func TestHeadSpanCobreDiretivaEArgumentos(t *testing.T) {
	tree := parseSimples(t)
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

func TestSpanDeDiretivaSimplesTerminaNoPontoEVirgula(t *testing.T) {
	tree := parseSimples(t)
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

func TestSpanDeBlocoTerminaNaChaveDeFechamento(t *testing.T) {
	tree := parseSimples(t)
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

	texto := string(src[upstream.Span.Start:upstream.Span.End])
	require.True(t, strings.HasPrefix(texto, "upstream backend_v1"))
	require.True(t, strings.HasSuffix(texto, "}"))
	require.Contains(t, texto, "server 10.0.0.1:8080;")

	require.Equal(t, "upstream backend_v1", string(src[upstream.HeadSpan.Start:upstream.HeadSpan.End]),
		"the head does not include the block")
}

func TestLinhaEColunaVemDoTokenizador(t *testing.T) {
	tree := parseSimples(t)

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

	linhas := strings.Split(string(tree.Files[0].Source), "\n")
	require.Contains(t, linhas[serverName.Line-1], "server_name")
}

// Quotes containing semicolons are the case that breaks a naive alignment.
func TestAlinhamentoSobreviveAAspasComPontoEVirgula(t *testing.T) {
	tree := parseSimples(t)
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
func TestSpansDeFilhosEstaoContidosNoPai(t *testing.T) {
	tree := parseSimples(t)

	var verificar func(nodes []*config.Node, pai *config.Node)
	verificar = func(nodes []*config.Node, pai *config.Node) {
		anteriorFim := -1
		for _, n := range nodes {
			if pai != nil {
				require.GreaterOrEqual(t, n.Span.Start, pai.Span.Start,
					"%s comeca antes do pai %s", n.Directive, pai.Directive)
				require.LessOrEqual(t, n.Span.End, pai.Span.End,
					"%s termina depois do pai %s", n.Directive, pai.Directive)
			}
			require.GreaterOrEqual(t, n.Span.Start, anteriorFim,
				"%s sobrepoe o irmao anterior", n.Directive)
			anteriorFim = n.Span.End
			verificar(n.Block, n)
		}
	}

	for _, f := range tree.Files {
		verificar(f.Nodes, nil)
	}
}

// Coverage: every significant byte of the file belongs to the span of some node.
// root level. And the concrete formulation of the property that supports the
// arquitetura: se ela vale, o casamento token-arvore esta correto.
func TestSpansRaizCobremTodoByteSignificativo(t *testing.T) {
	tree := parseSimples(t)
	src := tree.Files[0].Source

	coberto := make([]bool, len(src))
	for _, n := range tree.Files[0].Nodes {
		for i := n.Span.Start; i < n.Span.End; i++ {
			coberto[i] = true
		}
	}

	for i, b := range src {
		if coberto[i] {
			continue
		}
		require.True(t, b == ' ' || b == '\t' || b == '\n' || b == '\r',
			"byte %d (%q) on the uncovered line is not whitespace", i, string(b))
	}
}

func TestBlocoVazioEhReconhecido(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "vazio.conf")
	require.NoError(t, os.WriteFile(p, []byte("events {}\n"), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)

	events := tree.Files[0].Nodes[0]
	require.Equal(t, "events", events.Directive)
	require.True(t, events.HasBlock(), "events {} abre um bloco, mesmo vazio")
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

// align matches the semantic tree coming from the crossplane with the tokens from the
// arquivo, anexando offsets de byte a cada no.
//
// The match is sequential: the crossplane preserves the order of the document, and
// with ParseComments turned on until the comments are in the tree. So one
// A single simultaneous path solves everything — there is no search or heuristics.
func alinhar(f *File) error {
	toks, err := Tokenize(f.Source)
	if err != nil {
		return fmt.Errorf("ao tokenizar %s: %w", f.Path, err)
	}

	a := &aligner{file: f.Path, toks: toks}
	if err := a.nos(f.Nodes); err != nil {
		return err
	}
	if a.pos != len(a.toks) {
		return fmt.Errorf("%s: sobraram %d tokens apos alinhar a arvore",
			f.Path, len(a.toks)-a.pos)
	}
	return nil
}

type aligner struct {
	file string
	toks []Token
	pos  int
}

func (a *aligner) nos(nodes []*Node) error {
	for _, n := range nodes {
		if err := a.no(n); err != nil {
			return err
		}
	}
	return nil
}

func (a *aligner) no(n *Node) error {
	if n.IsComment() {
		tok, err := a.consumir(TokenComment)
		if err != nil {
			return err
		}
		n.Line, n.Column = tok.Line, tok.Column
		n.Span = Span{tok.Start, tok.End}
		n.HeadSpan = n.Span
		return nil
	}

	nome, err := a.consumir(TokenWord)
	if err != nil {
		return err
	}
	n.Line, n.Column = nome.Line, nome.Column

	fimDaCabeca := nome.End
	for range n.Args {
		arg, err := a.consumir(TokenWord)
		if err != nil {
			return err
		}
		fimDaCabeca = arg.End
	}
	n.HeadSpan = Span{nome.Start, fimDaCabeca}

	// Looking at the next token is more reliable than inspecting no.Block: a
	// empty block and indistinguishable from a simple directive by the Block field.
	proximo, err := a.espiar()
	if err != nil {
		return err
	}

	switch proximo.Kind {
	case TokenSemicolon:
		fim, _ := a.consumir(TokenSemicolon)
		n.temBloco = false
		n.Span = Span{nome.Start, fim.End}
		return nil

	case TokenBlockStart:
		if _, err := a.consumir(TokenBlockStart); err != nil {
			return err
		}
		if err := a.nos(n.Block); err != nil {
			return err
		}
		fim, err := a.consumir(TokenBlockEnd)
		if err != nil {
			return err
		}
		n.temBloco = true
		n.Span = Span{nome.Start, fim.End}
		return nil

	default:
		return fmt.Errorf("%s:%d: esperava ';' ou '{' apos %q, encontrei %q",
			a.file, proximo.Line, n.Directive, proximo.Raw)
	}
}

func (a *aligner) espiar() (Token, error) {
	if a.pos >= len(a.toks) {
		return Token{}, fmt.Errorf("%s: fim inesperado da configuracao", a.file)
	}
	return a.toks[a.pos], nil
}

func (a *aligner) consumir(kind TokenKind) (Token, error) {
	tok, err := a.espiar()
	if err != nil {
		return Token{}, err
	}
	if tok.Kind != kind {
		return Token{}, fmt.Errorf("%s:%d:%d: token inesperado %q",
			a.file, tok.Line, tok.Column, tok.Raw)
	}
	a.pos++
	return tok, nil
}
```

- [ ] **Step 4: Call the alignment in parse**

In `internal/config/parse.go`, inside the loop over `payload.Config`, replace the block that assembles the `File` with:

```go
		arquivo := &File{
			Path:   cfg.File,
			Source: src,
			Nodes:  converterDirectives(cfg.Parsed, cfg.File),
		}
		if err := alinhar(arquivo); err != nil {
			return nil, err
		}
		tree.Files = append(tree.Files, arquivo)
```

- [ ] **Step 5: Run the tests to check that they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — all, including the 8 new lineup.

- [ ] **Step 6: Run the alignment fuzz**

Add to `internal/config/fuzz_test.go`:

```go
// Align can never panic nor produce span out of bounds,
// whatever input the crossplane accepted.
func FuzzAlinhamento(f *testing.F) {
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
			n := len(arquivo.Source)
			tree.Walk(func(node *config.Node) bool {
				if node.Span.Start < 0 || node.Span.End > n || node.Span.Start > node.Span.End {
					t.Fatalf("invalid span [%d,%d) for a source of %d bytes",
						node.Span.Start, node.Span.End, n)
				}
				if node.HeadSpan.Start < node.Span.Start || node.HeadSpan.End > node.Span.End {
					t.Fatalf("head span fora do span do no")
				}
				return true
			})
		}
	})
}
```

Add the imports `os` and `path/filepath` to the fuzz file.

Run: `go test ./internal/config/ -run FuzzAlignment -fuzz FuzzAlignment -fuzztime 60s`
Expected: flawless. Cases found are in `testdata/fuzz/` as regression — commit them.

- [ ] **Step 7: Commit**

```bash
git add internal/config/
git commit -m "feat(config): alinhamento token-arvore com spans de byte"
```

---

### Task 10: Stable IDs

**Files:**
- Create: `internal/config/ids.go`
- Modify: `internal/config/parse.go` — assign IDs after alignment
- Test: `internal/config/ids_test.go`

**Interfaces:**
- Consumes: `config.Node`, `config.Tree` (Task 7)
- Produces: `config.AtribuirIDs(nodes []*Node, prefix string)`; `config.FindByID(t *Tree, id string) *Node`

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

func parseTexto(t *testing.T, conteudo string) *config.Tree {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "t.conf")
	require.NoError(t, os.WriteFile(p, []byte(conteudo), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)
	return tree
}

// Root-level context blocks do not have an index: they occur at most once.
func TestBlocosRaizNaoLevamIndice(t *testing.T) {
	tree := parseTexto(t, "events {}\nhttp {}\n")

	require.Equal(t, "e", tree.Files[0].Nodes[0].ID)
	require.Equal(t, "h", tree.Files[0].Nodes[1].ID)
}

func TestServersSaoNumeradosEntreSi(t *testing.T) {
	tree := parseTexto(t, `http {
  server { listen 80; }
  server { listen 443; }
}`)

	http := tree.Files[0].Nodes[0]
	require.Equal(t, "h.s0", http.Block[0].ID)
	require.Equal(t, "h.s1", http.Block[1].ID)
}

// The rule that reduces fragility: the index counts between siblings of the same type,
// not by absolute position. Entering a location does not renumber the servers.
func TestIndiceContaEntreIrmaosDoMesmoTipo(t *testing.T) {
	tree := parseTexto(t, `http {
  upstream a { server 10.0.0.1; }
  server { listen 80; }
  upstream b { server 10.0.0.2; }
  server { listen 443; }
}`)

	http := tree.Files[0].Nodes[0]
	require.Equal(t, "h.u0", http.Block[0].ID)
	require.Equal(t, "h.s0", http.Block[1].ID)
	require.Equal(t, "h.u1", http.Block[2].ID)
	require.Equal(t, "h.s1", http.Block[3].ID, "o segundo server continua sendo s1")
}

func TestDiretivasSimplesUsamPrefixoD(t *testing.T) {
	tree := parseTexto(t, `http {
  server {
    listen 443 ssl;
    server_name api.exemplo.com;
    location / { proxy_pass http://a; }
  }
}`)

	server := tree.Files[0].Nodes[0].Block[0]
	require.Equal(t, "h.s0.d0", server.Block[0].ID)
	require.Equal(t, "h.s0.d1", server.Block[1].ID)
	require.Equal(t, "h.s0.l0", server.Block[2].ID, "location tem abreviacao propria")
}

// Comments do not receive an ID and do not count in the index: if they did, add
// um comentario renumeraria as diretivas ao redor.
func TestComentariosNaoRecebemIDNemDeslocamIndices(t *testing.T) {
	tree := parseTexto(t, `http {
  server {
    # explica o listen
    listen 443 ssl;
    # explica o nome
    server_name api.exemplo.com;
  }
}`)

	server := tree.Files[0].Nodes[0].Block[0]

	require.Empty(t, server.Block[0].ID, "a comment has no ID")
	require.Equal(t, "h.s0.d0", server.Block[1].ID)
	require.Empty(t, server.Block[2].ID)
	require.Equal(t, "h.s0.d1", server.Block[3].ID, "the comment in between did not shift the index")
}

func TestLocationsAninhadasEncadeiamOID(t *testing.T) {
	tree := parseTexto(t, `http {
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

// Directives without abbreviations in the table use the full name, which maintains the ID
// legivel e evita colisao entre server e stream.
func TestDiretivaSemAbreviacaoUsaNomeCompleto(t *testing.T) {
	tree := parseTexto(t, `http {
  map $a $b { default 0; }
  stream { }
}`)

	http := tree.Files[0].Nodes[0]
	require.Equal(t, "h.mp0", http.Block[0].ID)
	require.Equal(t, "h.st0", http.Block[1].ID)
}

func TestFindByIDEncontraONo(t *testing.T) {
	tree := parseTexto(t, `http {
  server {
    location /api { proxy_pass http://backend; }
  }
}`)

	n := config.FindByID(tree, "h.s0.l0")

	require.NotNil(t, n)
	require.Equal(t, "location", n.Directive)
	require.Equal(t, []string{"/api"}, n.Args)
}

func TestFindByIDDevolveNilQuandoNaoAcha(t *testing.T) {
	tree := parseTexto(t, "http { server { listen 80; } }")

	require.Nil(t, config.FindByID(tree, "h.s9"))
}
```

- [ ] **Step 2: Run the test to verify that it fails**

Run: `go test ./internal/config/ -run "TestBlocos|TestServers|TestIndice|TestDiretivas|TestCommentarios|TestLocations|TestDiretiva|TestFindByID" -v`
Expected: FAIL — `undefined: config.FindByID`, empty IDs.

- [ ] **Step 3: Write the minimum implementation**

Create `internal/config/ids.go`:

```go
package config

import (
	"fmt"
	"strings"
)

// abreviacoes encurta as diretivas de bloco mais comuns. Primeira letra
// alone is not useful: server and stream would collide.
var abreviacoes = map[string]string{
	"http":     "h",
	"stream":   "st",
	"events":   "e",
	"mail":     "m",
	"server":   "s",
	"location": "l",
	"upstream": "u",
	"map":      "mp",
}

// Root blocks are the top contexts, which occur at most once per
// This does not require an index: the ID is "h", not "h0".
var blocosRaiz = map[string]bool{
	"http":   true,
	"stream": true,
	"events": true,
	"mail":   true,
}

// AtribuirIDs preenche o campo ID de cada no, recursivamente.
//
// The index counts between siblings of the same directive, not by absolute position:
// Entering a location does not renumber the servers next to it. No comments
// receive an ID and do not participate in the count, unless they add a comment
// would shift the IDs of neighboring directives.
func AtribuirIDs(nodes []*Node, prefixo string) {
	contadores := map[string]int{}
	naRaiz := prefixo == ""

	for _, n := range nodes {
		if n.IsComment() {
			continue
		}

		seg := segmento(n, contadores, naRaiz)
		if naRaiz {
			n.ID = seg
		} else {
			n.ID = prefixo + "." + seg
		}

		if len(n.Block) > 0 {
			AtribuirIDs(n.Block, n.ID)
		}
	}
}

func segmento(n *Node, contadores map[string]int, naRaiz bool) string {
	// So o nivel raiz dispensa indice: um stream aninhado dentro de http e
	// just another sister block and needs to be numbered normally.
	if naRaiz && n.HasBlock() && blocosRaiz[n.Directive] {
		return abreviar(n.Directive)
	}

	chave := n.Directive
	base := abreviar(n.Directive)
	if !n.HasBlock() && abreviacoes[n.Directive] == "" {
		// Simple directives without their own abbreviation share the d counter.
		chave, base = "", "d"
	}

	i := contadores[chave]
	contadores[chave] = i + 1
	return fmt.Sprintf("%s%d", base, i)
}

func abreviar(directive string) string {
	if a, ok := abreviacoes[directive]; ok {
		return a
	}
	return directive
}

// FindByID locates a node by its ID. Returns nil if it does not exist.
func FindByID(t *Tree, id string) *Node {
	id = strings.TrimPrefix(id, "#")

	var achado *Node
	t.Walk(func(n *Node) bool {
		if achado != nil {
			return false
		}
		if n.ID == id {
			achado = n
			return false
		}
		return true
	})
	return achado
}
```

- [ ] **Step 4: Assign IDs in the parse**

In `internal/config/parse.go`, right after `align(file)`:

```go
		AtribuirIDs(arquivo.Nodes, "")
```

- [ ] **Step 5: Run the tests to check that they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — 9 new ID tests plus all previous ones.

- [ ] **Step 6: Commit**

```bash
git add internal/config/ids.go internal/config/ids_test.go internal/config/parse.go
git commit -m "feat(config): IDs estaveis contados entre irmaos do mesmo tipo"
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

func TestHashTemPrefixoSha256(t *testing.T) {
	tree := parseTexto(t, "http { server { listen 80; } }")

	require.True(t, strings.HasPrefix(tree.Hash, "sha256:"))
	require.Len(t, tree.Hash, len("sha256:")+64)
}

func TestHashEhDeterministico(t *testing.T) {
	a := parseTexto(t, "http { server { listen 80; } }")
	b := parseTexto(t, "http { server { listen 80; } }")

	require.Equal(t, a.Hash, b.Hash)
}

// The hash protects the meaning, not the text: two configurations that only differ
// em formatacao precisam produzir o mesmo hash, senao rodar fmt invalidaria
// all IDs that the agent is holding.
func TestFormatacaoDiferenteProduzMesmoHash(t *testing.T) {
	compacto := parseTexto(t, "http{server{listen 80;}}")
	espacado := parseTexto(t, `
http {
    server {
        listen 80;
    }
}
`)

	require.Equal(t, compacto.Hash, espacado.Hash)
}

func TestComentariosNaoEntramNoHash(t *testing.T) {
	sem := parseTexto(t, "http { server { listen 80; } }")
	com := parseTexto(t, `# um comentario
http {
  # outro
  server { listen 80; }
}`)

	require.Equal(t, sem.Hash, com.Hash)
}

func TestMudancaDeArgumentoMudaOHash(t *testing.T) {
	a := parseTexto(t, "http { server { listen 80; } }")
	b := parseTexto(t, "http { server { listen 443; } }")

	require.NotEqual(t, a.Hash, b.Hash)
}

// Order matters: moving a server changes the meaning of IDs, so you need to
// mudar o hash.
func TestOrdemDeBlocosMudaOHash(t *testing.T) {
	a := parseTexto(t, "http { server { listen 80; } server { listen 443; } }")
	b := parseTexto(t, "http { server { listen 443; } server { listen 80; } }")

	require.NotEqual(t, a.Hash, b.Hash)
}

// Without a separator between directive and arguments, "a b" and "ab" would collide.
func TestDiretivasDiferentesNaoColidem(t *testing.T) {
	a := parseTexto(t, "ab c;")
	b := parseTexto(t, "a bc;")

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
// are left out, so running fmt does not invalidate the IDs that the agent is
// holding. Now the order of the blocks comes into play, because moving a server changes what
// in each ID refers.
func Hash(t *Tree) string {
	h := sha256.New()
	for _, f := range t.Files {
		escreverCampo(h, f.Path)
		escreverNodes(h, f.Nodes)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func escreverNodes(h hash.Hash, nodes []*Node) {
	for _, n := range nodes {
		if n.IsComment() {
			continue
		}
		escreverCampo(h, n.Directive)
		escreverCampo(h, strconv.Itoa(len(n.Args)))
		for _, a := range n.Args {
			escreverCampo(h, a)
		}
		if n.HasBlock() {
			escreverCampo(h, "{")
			escreverNodes(h, n.Block)
			escreverCampo(h, "}")
		} else {
			escreverCampo(h, ";")
		}
	}
}

// writeField uses a separator that cannot appear in a directive, to
// that "ab c" and "a bc" never collide.
func escreverCampo(h hash.Hash, s string) {
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

> `TestFormatacaoDiferenteProduzEvenHash` and `TestCommentariosNaoEntramNoHash` use different files in `t.TempDir()`, and the file path enters the hash via `epilarCampo(h, f.Path)`. This will cause both tests to fail. The fix is to only use the **base name** of the file in the hash, not the absolute path — which is also the right behavior: moving the directory configuration doesn't change its meaning. Apply this correction in this step; do not change the tests.

- [ ] **Step 6: Commit**

```bash
git add internal/config/hash.go internal/config/hash_test.go internal/config/parse.go
git commit -m "feat(config): hash canonico ancorando os IDs"
```

---

### Task 12: Resolving include with source tracking

- Test: `internal/config/combine_test.go`, `internal/config/testdata/combine/nginx.conf`, `internal/config/testdata/combine/conf.d/api.conf`**Files:**
- Create: `internal/config/combine.go`

- Produces: `config.Combine(t *Tree) (*Tree, error)` — returns a new tree with a single `File`, where each `include` has been replaced by the included file nodes and each node carries `Origin`**Interfaces:**
- Consumes: `config.Tree`, `config.File`, `config.Node`, `config.Origin`, `config.AtribuirIDs` (Tasks 7, 10)

- [ ] **Step 1: Create the fixtures**

Create `internal/config/testdata/combine/nginx.conf`:

```nginx
events {}

http {
    include conf.d/api.conf;

    server {
        listen 80;
        server_name legado.exemplo.com;
    }
}
```

Create `internal/config/testdata/combine/conf.d/api.conf`:

```nginx
server {
    listen 443 ssl;
    server_name api.exemplo.com;
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

func TestParseSemCombineMantemArquivosSeparados(t *testing.T) {
	tree := parseCombine(t)

	require.Len(t, tree.Files, 2, "nginx.conf e conf.d/api.conf")
}

func TestCombineProduzUmUnicoArquivo(t *testing.T) {
	combinado, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	require.Len(t, combinado.Files, 1)
}

func TestCombineSubstituiIncludePelosNosIncluidos(t *testing.T) {
	combinado, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var http *config.Node
	combinado.Walk(func(n *config.Node) bool {
		if n.Directive == "http" {
			http = n
			return false
		}
		return true
	})
	require.NotNil(t, http)

	var nomes []string
	for _, filho := range http.Block {
		nomes = append(nomes, filho.Directive)
	}
	require.Equal(t, []string{"server", "server"}, nomes,
		"the include is gone and became the server of the included file")
}

// Origin and what allows the agent to know which actual file to edit next
// de ver a configuracao resolvida.
func TestCombinePreencheOriginComOArquivoReal(t *testing.T) {
	combinado, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var api *config.Node
	combinado.Walk(func(n *config.Node) bool {
		if n.Directive == "server_name" && len(n.Args) > 0 && n.Args[0] == "api.exemplo.com" {
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

func TestCombineMantemOriginDoArquivoPrincipal(t *testing.T) {
	combinado, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	var legado *config.Node
	combinado.Walk(func(n *config.Node) bool {
		if n.Directive == "server_name" && len(n.Args) > 0 && n.Args[0] == "legado.exemplo.com" {
			legado = n
			return false
		}
		return true
	})
	require.NotNil(t, legado)

	require.NotNil(t, legado.Origin)
	require.Contains(t, legado.Origin.File, "nginx.conf")
}

// The combined tree IDs are renumbered over the resolved structure:
// and this is the structure that the agent sees and upon which he operates.
func TestCombineRenumeraIDsSobreAEstruturaResolvida(t *testing.T) {
	combinado, err := config.Combine(parseCombine(t))
	require.NoError(t, err)

	api := config.FindByID(combinado, "h.s0")
	require.NotNil(t, api)
	require.Equal(t, "server", api.Directive)
	require.Contains(t, api.Origin.File, "api.conf",
		"o primeiro server da arvore resolvida vem do include")

	legado := config.FindByID(combinado, "h.s1")
	require.NotNil(t, legado)
	require.Contains(t, legado.Origin.File, "nginx.conf")
}

// The hash of the combined tree differs from that of the non-combined one: they are views
// different, and confusing them would invalidate IDs for no reason.
func TestCombineRecalculaOHash(t *testing.T) {
	original := parseCombine(t)
	combinado, err := config.Combine(original)
	require.NoError(t, err)

	require.NotEmpty(t, combinado.Hash)
	require.NotEqual(t, original.Hash, combinado.Hash)
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
// each no carries the real origin.
//
// The resolution is done over our tree, and not by CombineConfigs.
// crossplane, because matching before would destroy the spans: they point to
// offsets de arquivos especificos. Aqui os nos originais permanecem intactos
// e apenas a estrutura e reorganizada.
func Combine(t *Tree) (*Tree, error) {
	if len(t.Files) == 0 {
		return &Tree{}, nil
	}

	principal := t.Files[0]
	c := &combinador{arquivos: t.Files, visitados: map[string]bool{}}

	nodes, err := c.resolver(principal)
	if err != nil {
		return nil, err
	}

	combinado := &Tree{
		Files: []*File{{
			Path:   principal.Path,
			Source: principal.Source,
			Nodes:  nodes,
		}},
	}
	AtribuirIDs(combinado.Files[0].Nodes, "")
	combinado.Hash = Hash(combinado)
	return combinado, nil
}

// files are a slice, not a map, on purpose: an include with glob can
// casar varios arquivos, e iterar um map daria ordem diferente a cada
// execution — which would cause the IDs and hash to change without the configuration changing.
type combinador struct {
	arquivos  []*File
	visitados map[string]bool
}

func (c *combinador) resolver(f *File) ([]*Node, error) {
	if c.visitados[f.Path] {
		return nil, fmt.Errorf("include circular detectado em %s", f.Path)
	}
	c.visitados[f.Path] = true
	defer delete(c.visitados, f.Path)

	return c.expandir(f.Nodes)
}

func (c *combinador) expandir(nodes []*Node) ([]*Node, error) {
	var saida []*Node

	for _, n := range nodes {
		if n.Directive == "include" {
			incluidos, err := c.expandirInclude(n)
			if err != nil {
				return nil, err
			}
			saida = append(saida, incluidos...)
			continue
		}

		copia := *n
		copia.Origin = &Origin{File: n.File, Line: n.Line}

		if len(n.Block) > 0 {
			filhos, err := c.expandir(n.Block)
			if err != nil {
				return nil, err
			}
			copia.Block = filhos
		}
		saida = append(saida, &copia)
	}

	return saida, nil
}

// expandInclude finds files that match the include pattern.
// Crossplane already resolved the globs and returned each matched file as a
// Own configuration, so just find the ones that haven't been consumed yet.
func (c *combinador) expandirInclude(n *Node) ([]*Node, error) {
	var saida []*Node

	for _, alvo := range c.arquivosDoInclude(n) {
		nodes, err := c.resolver(alvo)
		if err != nil {
			return nil, err
		}
		saida = append(saida, nodes...)
	}

	return saida, nil
}

// The iteration is over the slice of files, in the order in which the crossplane
// returned, so that the result is deterministic.
func (c *combinador) arquivosDoInclude(n *Node) []*File {
	var achados []*File
	for _, f := range c.arquivos {
		for _, arg := range n.Args {
			if casaInclude(f.Path, arg, n.File) {
				achados = append(achados, f)
				break
			}
		}
	}
	return achados
}
```

Also create, in the same file, the path matching:

```go
// matchInclude decides whether a parsed file corresponds to the pattern of an
// include. The pattern may be relative to the file that declared it.
func casaInclude(caminho, padrao, declaradoEm string) bool {
	if caminho == padrao {
		return true
	}
	base := filepath.Dir(declaradoEm)
	if ok, _ := filepath.Match(filepath.Join(base, padrao), caminho); ok {
		return true
	}
	if ok, _ := filepath.Match(padrao, caminho); ok {
		return true
	}
	return false
}
```

Add `"path/filepath"` to imports.

- [ ] **Step 5: Run the tests to check that they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — 7 combine tests plus all of the above.

> `TestCombineRenumeraIDsSobreAEstruturaResolvida` requires that the `server` of the included file comes **before** the `server` declared in `nginx.conf`, because `include` appears before. If it fails, the problem is the order in `expand`, not the test.

- [ ] **Step 6: Commit**

```bash
git add internal/config/combine.go internal/config/combine_test.go internal/config/testdata/combine/
git commit -m "feat(config): resolucao de include com rastreio de origem"
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
        server_name api.exemplo.com;
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

func rodarInspect(t *testing.T, args ...string) (output.ExitCode, *output.Envelope, string) {
	t.Helper()
	var out, errBuf bytes.Buffer

	code := cli.Execute(args, &out, &errBuf, false)

	var env output.Envelope
	if out.Len() > 0 {
		require.NoError(t, json.Unmarshal(out.Bytes(), &env), "saida: %s", out.String())
	}
	return code, &env, out.String()
}

func fixture(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "exemplo.conf")
}

func TestInspectRetornaSucesso(t *testing.T) {
	code, env, _ := rodarInspect(t, "inspect", "-c", fixture(t))

	require.Equal(t, output.ExitOK, code)
	require.True(t, env.OK)
	require.Equal(t, "inspect", env.Command)
}

// The hash in the meta is the anchor of the IDs that come out in the data.
func TestInspectPublicaOConfigHashNoMeta(t *testing.T) {
	_, env, _ := rodarInspect(t, "inspect", "-c", fixture(t))

	require.NotEmpty(t, env.Meta.ConfigHash)
	require.Contains(t, env.Meta.ConfigHash, "sha256:")
}

func TestInspectResumeAConfiguracao(t *testing.T) {
	_, _, bruto := rodarInspect(t, "inspect", "-c", fixture(t))

	var resposta struct {
		Data struct {
			Summary cli.Summary `json:"summary"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(bruto), &resposta))

	require.Equal(t, 1, resposta.Data.Summary.Servers)
	require.Equal(t, 2, resposta.Data.Summary.Locations)
	require.Equal(t, 1, resposta.Data.Summary.Upstreams)
	require.Equal(t, 1, resposta.Data.Summary.Files)
}

// The IDs must be in JSON: it is through them that the agent references a node in the
// chamada seguinte.
func TestInspectEmiteIDsNaArvore(t *testing.T) {
	_, _, bruto := rodarInspect(t, "inspect", "-c", fixture(t))

	require.Contains(t, bruto, `"id":"h.s0"`)
	require.Contains(t, bruto, `"id":"h.s0.l0"`)
	require.Contains(t, bruto, `"id":"h.u0"`)
}

func TestInspectEmiteSpans(t *testing.T) {
	_, _, bruto := rodarInspect(t, "inspect", "-c", fixture(t))

	require.Contains(t, bruto, `"span"`)
	require.Contains(t, bruto, `"head_span"`)
}

// The test that closes the writing cycle: the sensitive value cannot appear in the
// saida, mas a diretiva sim.
func TestInspectRedigeChavePrivada(t *testing.T) {
	_, _, bruto := rodarInspect(t, "inspect", "-c", fixture(t))

	require.NotContains(t, bruto, "/etc/ssl/private/api.key")
	require.Contains(t, bruto, "ssl_certificate_key", "a diretiva continua visivel")
	require.Contains(t, bruto, output.RedactedValue)
}

// Nonexistent file and IO failure, not usage error: the flag was correct,
// the disk and it didn't have the file.
func TestInspectComArquivoInexistenteEhFalhaInterna(t *testing.T) {
	code, env, _ := rodarInspect(t, "inspect", "-c", "testdata/nao-existe.conf")

	require.Equal(t, output.ExitInternal, code)
	require.False(t, env.OK)
}

func TestInspectSemNenhumaConfigEhErroDeUso(t *testing.T) {
	code, env, _ := rodarInspect(t, "inspect")

	require.Equal(t, output.ExitUsage, code)
	require.False(t, env.OK)
}

func TestInspectCombineResolveIncludes(t *testing.T) {
	code, _, bruto := rodarInspect(t, "inspect", "--combine",
		"-c", filepath.Join("..", "config", "testdata", "combine", "nginx.conf"))

	require.Equal(t, output.ExitOK, code)
	require.Contains(t, bruto, `"origin"`)
	require.NotContains(t, bruto, `"directive":"include"`,
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

// InspectData and the full dump: tree plus summary.
type InspectData struct {
	Config  []*config.File `json:"config"`
	Summary Summary        `json:"summary"`
}

// Redacted returns a copy with the sensitive values replaced. The copy and
// profunda nos nos afetados: a arvore original nunca e alterada, senao um fmt
// later would write *** to the user's file.
func (d InspectData) Redacted(rs output.RedactSet) any {
	if rs.Empty() {
		return d
	}

	arquivos := make([]*config.File, 0, len(d.Config))
	for _, f := range d.Config {
		arquivos = append(arquivos, &config.File{
			Path:   f.Path,
			Source: f.Source,
			Nodes:  redigirNodes(f.Nodes, rs),
		})
	}
	return InspectData{Config: arquivos, Summary: d.Summary}
}

func redigirNodes(nodes []*config.Node, rs output.RedactSet) []*config.Node {
	saida := make([]*config.Node, 0, len(nodes))
	for _, n := range nodes {
		copia := *n
		if rs.Matches(n.Directive, n.Args) {
			copia.Args = []string{output.RedactedValue}
		}
		if len(n.Block) > 0 {
			copia.Block = redigirNodes(n.Block, rs)
		}
		saida = append(saida, &copia)
	}
	return saida
}

func newInspectCmd(ctx *Context) *cobra.Command {
	var combine bool

	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Dump completo: arvore de configuracao e resumo",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			caminho := caminhoDaConfig(ctx)
			if caminho == "" {
				return output.Usage("informe a configuracao com -c ou em nginx.config")
			}

			tree, err := config.Parse(config.ParseOptions{Path: caminho})
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
			env.Data = InspectData{Config: tree.Files, Summary: resumir(tree)}
			env.Meta.ConfigHash = tree.Hash
			return ctx.Renderer.Render(env)
		},
	}

	cmd.Flags().BoolVar(&combine, "combine", false, "resolve os includes numa arvore unica")
	return cmd
}

func caminhoDaConfig(ctx *Context) string {
	if ctx.Flags.ConfigPath != "" {
		return ctx.Flags.ConfigPath
	}
	if ctx.Settings != nil {
		return ctx.Settings.Nginx.Config
	}
	return ""
}

func resumir(t *config.Tree) Summary {
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

> `TestInspectResumeAConfiguracao` counts `server` as directive. The fixture has `server 10.0.0.1:8080;` **inside** the upstream, which is also called `server`. If the test counts 2 servers, the fix is to only count `server` that opens block (`n.HasBlock()`), which is also the correct behavior — `server` inside `upstream` is another directive. Apply the fix; do not change the test.

- [ ] **Step 6: Check torque by hand**

```bash
go build -o /tmp/ngx ./cmd/ngx
/tmp/ngx inspect -c internal/cli/testdata/exemplo.conf | head -c 400; echo
/tmp/ngx inspect -c internal/cli/testdata/exemplo.conf | grep -c 'private/api.key'
```

Expected: a JSON envelope with the tree; `grep -c` prints `0`, confirming that the private key was not leaked.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/ 
git commit -m "feat(cli): comando inspect"
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
	go test ./internal/config/ -run FuzzAlinhamento -fuzz FuzzAlinhamento -fuzztime 60s

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
quando o reload derruba produção. O `ngx` unifica parse, análise e mutação num
binário único, com saída JSON estruturada por padrão, leitura por seletor em vez
de dump de milhares de linhas, e mudanças transacionais com rollback automático.

## Estado

**v0.1, em construção.** Somente leitura: nenhum comando altera a configuração de
um servidor em execução. A mutação transacional chega na v0.2.

Funcionando hoje:

- `ngx inspect` — árvore completa de configuração, com IDs estáveis, offsets de
  byte e resumo
- `ngx version`

## Exemplo

```console
$ ngx inspect -c /etc/nginx/nginx.conf | jq '.data.summary'
{
  "files": 4,
  "servers": 12,
  "locations": 37,
  "upstreams": 5
}
```

Valores sensíveis — chaves privadas, headers de autorização — são redigidos por
padrão antes de sair. `--no-redact` só é aceito quando a saída é um terminal.

## Construindo

```console
$ make fuzz # tokenizer and alignment fuzzers$ make build # compiles to bin/ngx
$ make test # complete suite with race detector
```

Requires Go 1.25. `.tool-versions` pins the version for asdf users.

## Design

The architecture decisions and the reasoning behind each are in
[`docs/superpowers/specs/`](docs/superpowers/specs/).

## Licença

MIT. Copyright (c) 2026 Eduardo Benck.
````

- [ ] **Step 3: Run the complete suite with race detector**

Run: `make vet && make test`
Expected: PASS on all packages, without vet warnings.

- [ ] **Step 4: Run the fuzzers**

Run: `make fuzz`
Expected: flawless. New cases in `testdata/fuzz/` must be committed as regression.

- [ ] **Step 5: Commit**

```bash
git add README.md Makefile
git commit -m "chore: makefile e readme"
```

---

## Spec coverage check

| Seção da spec | Task |
|---|---|
| §3 arquitetura, regra de camada | 1, 4, 6 |
| §4.1 modelo `Node`, dois spans | 7, 9 |
| §4.2 IDs entre irmãos do mesmo tipo | 10 |
| §4.3 hash canônico | 11 |
| §5 R1–R4 seletores | **Plano 2** |
| §6.1 envelope | 1 |
| §6.2 exit codes | 2 |
| §6.3 redação, `--no-redact` em TTY | 3, 4, 13 |
| §7 runtime, drift | **Plano 3** |
| §8 `inspect` | 13 |
| §8 `get`, `tree` | **Plano 2** |
| §8 `status`, `fmt`, `test`, `diff` | **Plano 3** |
| §8.1 configuration file | 5 |
| §9 property test de spans | 8, 9 |
| §9 fuzzing | 8, 9 |
| §9 golden files, fake nginx, integração | **Plano 3** |
| §10 repositório, licença | 1, 14 |
| §10 CI e goreleaser | **Plano 3** |

**Refinement of §9 of the spec:** the spec describes the property of spans as "reconstituting the file byte by byte". The concrete and verifiable formulation adopted here is stronger and is in `TestSpansRaizCobremTodoByteSignificativo` plus `TestSpansDeFilhosEstaoContidosNoPai`: every non-white byte belongs to the span of some root node, spans of children are contained in those of parents, and siblings do not overlap. It's worth updating the spec for this wording when Plan 1 closes.
