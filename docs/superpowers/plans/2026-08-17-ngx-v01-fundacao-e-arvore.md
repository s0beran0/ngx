# ngx v0.1 — Plano 1: Fundação e Árvore

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Construir a fundação do `ngx` — envelope JSON, exit codes, redação, configuração — e a árvore canônica de configuração nginx com spans de byte, IDs estáveis e hash, entregando o comando `ngx inspect` funcionando ponta a ponta.

**Architecture:** `nginx-go-crossplane` fornece a árvore semântica e a validação de diretivas; um tokenizador próprio fornece os offsets de byte; as duas estruturas são casadas por sequência de tokens em ordem de documento. A camada `output` é a única que serializa e a única que decide exit code; comandos devolvem valores tipados e erros tipados.

**Tech Stack:** Go 1.25, `nginx-go-crossplane`, `cobra`, `koanf/v2`, `testify`.

**Spec:** `docs/superpowers/specs/2026-08-17-ngx-cli-design.md`

## Global Constraints

- Módulo Go: `github.com/eduardoborges/ngx`. Go 1.25 (`.tool-versions` já fixa `golang 1.25.9`).
- **Zero CGO.** Nenhuma dependência que exija cgo.
- Licença MIT em nome de Eduardo Benck. Sem qualquer menção, branding ou copyright da SEA Tecnologia.
- **Mensagens de commit nunca mencionam Claude ou IA.** Sem trailer `Co-Authored-By`, sem "Generated with". Autoria exclusiva do Eduardo.
- Nenhum `exec` de shell. Toda invocação externa usa `exec.Command` com argv explícito.
- Todo campo JSON de lista serializa como `[]`, nunca `null` — um agente que faz `.length` numa lista nula quebra.
- Campo desconhecido ou indisponível é **omitido**, nunca estimado ou preenchido com valor falso.
- Comentários de código em português, como o resto do projeto.

---

### Task 1: Bootstrap do módulo e envelope de saída

**Files:**
- Create: `go.mod`, `LICENSE`, `internal/output/envelope.go`
- Test: `internal/output/envelope_test.go`

**Interfaces:**
- Consumes: nada (primeira tarefa)
- Produces: `output.Envelope`, `output.Diagnostic`, `output.Meta`, `output.Severity`, `output.New(command string) *Envelope`, `(*Envelope).AddDiagnostic(Diagnostic)`, `output.Version`

- [ ] **Step 1: Inicializar o módulo e a licença**

```bash
go mod init github.com/eduardoborges/ngx
go get github.com/stretchr/testify@latest
```

Criar `LICENSE` com o texto padrão MIT, `Copyright (c) 2026 Eduardo Benck`.

- [ ] **Step 2: Escrever o teste que falha**

Criar `internal/output/envelope_test.go`:

```go
package output_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eduardoborges/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// Um agente consumindo a saída faz `.diagnostics.length`. Uma lista nula
// quebra esse acesso, então lista vazia precisa serializar como [].
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

// A severidade error é o que derruba o ok do envelope. Warning e info não.
func TestAddDiagnosticErrorDerrubaOK(t *testing.T) {
	env := output.New("test")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityWarning, Message: "cuidado"})
	require.True(t, env.OK, "warning nao deve derrubar ok")

	env.AddDiagnostic(output.Diagnostic{Severity: output.SeverityError, Message: "falhou"})
	require.False(t, env.OK, "error deve derrubar ok")

	require.Len(t, env.Diagnostics, 2)
}

// Campos opcionais ausentes nao devem poluir a saida do agente.
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

- [ ] **Step 3: Rodar o teste para verificar que falha**

Run: `go test ./internal/output/ -run TestEnvelope -v`
Expected: FAIL — o pacote `internal/output` não existe ainda.

- [ ] **Step 4: Escrever a implementação mínima**

Criar `internal/output/envelope.go`:

```go
// Package output define o envelope de saida do ngx, os diagnosticos e a
// traducao de erro para exit code. E a unica camada que serializa.
package output

// Version e a versao do ngx. Sobrescrita no build via -ldflags.
var Version = "0.1.0-dev"

// Severity classifica um diagnostico. Apenas SeverityError derruba o ok
// do envelope.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Diagnostic e um achado localizado. Os campos selector e id existem para
// que o agente aja sobre o achado sem reparsear a configuracao.
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
// nesta resposta: um ID so e valido contra o hash que veio junto com ele.
type Meta struct {
	DurationMS   int64  `json:"duration_ms"`
	NginxVersion string `json:"nginx_version,omitempty"`
	ConfigHash   string `json:"config_hash,omitempty"`
}

// Envelope e o formato unico de toda saida JSON do ngx.
type Envelope struct {
	OK          bool         `json:"ok"`
	Command     string       `json:"command"`
	NgxVersion  string       `json:"ngx_version"`
	Data        any          `json:"data"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Meta        Meta         `json:"meta"`
}

// New cria um envelope de sucesso para o comando dado.
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

- [ ] **Step 5: Rodar os testes para verificar que passam**

Run: `go test ./internal/output/ -v`
Expected: PASS — 4 testes.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum LICENSE internal/output/
git commit -m "feat(output): envelope de saida e diagnosticos"
```

---

### Task 2: Erros tipados e exit codes

**Files:**
- Create: `internal/output/errors.go`
- Test: `internal/output/errors_test.go`

**Interfaces:**
- Consumes: `output.Diagnostic`, `output.SeverityError` (Task 1)
- Produces: `output.ExitCode` e as constantes `ExitOK`/`ExitInternal`/`ExitUsage`/`ExitInvalidConfig`/`ExitDrift`/`ExitHashMismatch`; `output.Error` com campos `Code`/`Diag`/`Err`; construtores `output.Usage`, `output.Internal`, `output.InvalidConfig`, `output.Drift`, `output.HashMismatch`; `output.CodeOf(error) ExitCode`

- [ ] **Step 1: Escrever o teste que falha**

Criar `internal/output/errors_test.go`:

```go
package output_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/eduardoborges/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

func TestCodeOfNilEhSucesso(t *testing.T) {
	require.Equal(t, output.ExitOK, output.CodeOf(nil))
}

// Um erro que nao carrega codigo e um erro interno, nao um sucesso.
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

// O codigo precisa sobreviver ao wrapping, senao um erro embrulhado por uma
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

// HashMismatch e o erro que impede o agente de agir sobre um ID envelhecido.
// A mensagem precisa mostrar os dois hashes para ele saber o que aconteceu.
func TestHashMismatchMostraOsDoisHashes(t *testing.T) {
	err := output.HashMismatch("sha256:esperado", "sha256:atual")

	require.Contains(t, err.Error(), "sha256:esperado")
	require.Contains(t, err.Error(), "sha256:atual")
}
```

- [ ] **Step 2: Rodar o teste para verificar que falha**

Run: `go test ./internal/output/ -run "TestCodeOf|TestConstrutores|TestErro|TestHashMismatch" -v`
Expected: FAIL — `undefined: output.ExitOK`, `undefined: output.CodeOf` etc.

- [ ] **Step 3: Escrever a implementação mínima**

Criar `internal/output/errors.go`:

```go
package output

import (
	"errors"
	"fmt"
)

// ExitCode e o codigo de saida do processo. A v0.1 emite apenas os codigos
// abaixo; 4 (lint), 5 e 6 (apply) e 8 (mutacao ambigua) pertencem a comandos
// que ainda nao existem e nao sao documentados como suportados ate que sejam
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

// Error e um erro que carrega seu proprio exit code e o diagnostico
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

// InvalidConfig sinaliza que a configuracao do nginx nao e valida.
func InvalidConfig(format string, args ...any) *Error {
	return newError(ExitInvalidConfig, "NGX-0003", format, args...)
}

// Drift sinaliza que a configuracao em disco difere da que esta carregada.
func Drift(format string, args ...any) *Error {
	return newError(ExitDrift, "NGX-0007", format, args...)
}

// HashMismatch sinaliza que um ID foi apresentado contra uma versao da
// configuracao diferente daquela em que foi gerado. Os IDs anteriores sao
// invalidos e o agente precisa reler antes de agir.
func HashMismatch(esperado, atual string) *Error {
	return newError(ExitHashMismatch, "NGX-0009",
		"a configuracao mudou desde a leitura: esperado %s, atual %s", esperado, atual)
}

// Internal envolve uma falha de IO ou um defeito do proprio ngx.
func Internal(err error, format string, args ...any) *Error {
	e := newError(ExitInternal, "NGX-0001", format, args...)
	e.Err = err
	return e
}

// CodeOf extrai o exit code de um erro, atravessando wrapping. Um erro sem
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

- [ ] **Step 4: Rodar os testes para verificar que passam**

Run: `go test ./internal/output/ -v`
Expected: PASS — todos os testes de Task 1 e Task 2.

- [ ] **Step 5: Commit**

```bash
git add internal/output/errors.go internal/output/errors_test.go
git commit -m "feat(output): erros tipados carregando exit code"
```

---

### Task 3: Redação de valores sensíveis

**Files:**
- Create: `internal/output/redact.go`
- Test: `internal/output/redact_test.go`

**Interfaces:**
- Consumes: nada de tasks anteriores
- Produces: `output.RedactRule` com `Matches(directive string, args []string) bool`; `output.RedactSet` com `Matches(directive string, args []string) bool` e `Empty() bool`; `output.ParseRedactRule(string) (RedactRule, error)`; `output.NewRedactSet([]string) (RedactSet, error)`; `output.RedactedValue` (a constante `"***"`); a interface `output.Redactable { Redacted(RedactSet) any }`

- [ ] **Step 1: Escrever o teste que falha**

Criar `internal/output/redact_test.go`:

```go
package output_test

import (
	"testing"

	"github.com/eduardoborges/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// A spec usa tres formatos para a mesma coisa. Todos precisam funcionar como
// escritos, para que uma configuracao copiada da spec nao falhe em silencio.
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
		"outro header nao deve ser redigido")
	require.False(t, r.Matches("proxy_set_header", nil),
		"sem args nao pode casar uma regra que exige prefixo")
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

- [ ] **Step 2: Rodar o teste para verificar que falha**

Run: `go test ./internal/output/ -run Redact -v`
Expected: FAIL — `undefined: output.ParseRedactRule`.

- [ ] **Step 3: Escrever a implementação mínima**

Criar `internal/output/redact.go`:

```go
package output

import (
	"fmt"
	"strings"
)

// RedactedValue substitui o valor de uma diretiva sensivel. A diretiva, o id
// e a linha permanecem visiveis: sumir com o no inteiro faria o agente
// concluir que a diretiva nao existe, o que e pior que esconder o valor.
const RedactedValue = "***"

// Redactable e implementado por qualquer dado que saiba produzir uma copia
// redigida de si mesmo. A redacao acontece na serializacao, nunca na arvore
// em memoria: se a arvore fosse redigida no parse, fmt gravaria *** dentro
// do .conf do usuario.
type Redactable interface {
	Redacted(rs RedactSet) any
}

// RedactRule casa uma diretiva pelo nome, opcionalmente exigindo um prefixo
// de argumentos.
type RedactRule struct {
	Directive string
	ArgPrefix []string
}

// ParseRedactRule le uma entrada de output.redact. Aceita os tres formatos
// que a spec usa: nome de diretiva, nome com prefixo de argumentos, e o
// prefixo de contexto "**." — que e redundante, porque regras ja valem em
// qualquer contexto, mas e aceito para nao quebrar configuracoes escritas a
// partir da spec.
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

// RedactSet e o conjunto de regras ativas.
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

- [ ] **Step 4: Rodar os testes para verificar que passam**

Run: `go test ./internal/output/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/output/redact.go internal/output/redact_test.go
git commit -m "feat(output): redacao de valores sensiveis"
```

---

### Task 4: Renderers e o portão de `--no-redact`

**Files:**
- Create: `internal/output/render.go`
- Test: `internal/output/render_test.go`

**Interfaces:**
- Consumes: `output.Envelope` (Task 1), `output.Usage` (Task 2), `output.RedactSet`, `output.Redactable` (Task 3)
- Produces: `output.Format` com `FormatAuto`/`FormatJSON`/`FormatHuman`; `output.Renderer` com campos `Out`, `Format`, `IsTTY`, `Redact`, `NoRedact`, `Quiet` e método `Render(*Envelope) error`; a interface `output.HumanRenderable { RenderHuman(io.Writer) error }`

- [ ] **Step 1: Escrever o teste que falha**

Criar `internal/output/render_test.go`:

```go
package output_test

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/eduardoborges/ngx/internal/output"
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

// Formato auto sem TTY tem que virar JSON: e o caso do agente lendo um pipe.
func TestFormatAutoSemTTYProduzJSON(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatAuto, IsTTY: false}

	require.NoError(t, r.Render(output.New("status")))

	var env output.Envelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.Equal(t, "status", env.Command)
}

// Com TTY e sem renderer humano no dado, cai para JSON indentado em vez de
// imprimir a struct crua do Go.
func TestFormatAutoComTTYUsaRenderHumanQuandoDisponivel(t *testing.T) {
	var buf bytes.Buffer
	r := &output.Renderer{Out: &buf, Format: output.FormatAuto, IsTTY: true}

	env := output.New("status")
	env.Data = dadoHumano{}
	require.NoError(t, r.Render(env))

	require.Contains(t, buf.String(), "saida humana")
}

// O portao que a redacao existe para fechar: um humano no terminal pode ver o
// segredo, um agente lendo o pipe nao consegue nem pedir.
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

// Sem --no-redact, o dado passa pela redacao antes de ser serializado.
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
// saber o que deu errado.
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

- [ ] **Step 2: Rodar o teste para verificar que falha**

Run: `go test ./internal/output/ -run "TestFormat|TestNoRedact|TestRender|TestQuiet" -v`
Expected: FAIL — `undefined: output.Renderer`.

- [ ] **Step 3: Escrever a implementação mínima**

Criar `internal/output/render.go`:

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

// HumanRenderable e implementado por dados que sabem se apresentar a um
// humano. Dados que nao implementam caem para JSON indentado, que e mais
// util que imprimir a struct crua do Go.
type HumanRenderable interface {
	RenderHuman(w io.Writer) error
}

// Renderer serializa o envelope. E a unica camada que escreve saida.
type Renderer struct {
	Out      io.Writer
	Format   Format
	IsTTY    bool
	Redact   RedactSet
	NoRedact bool
	Quiet    bool
}

// Render escreve o envelope no formato resolvido.
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

- [ ] **Step 4: Rodar os testes para verificar que passam**

Run: `go test ./internal/output/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/output/render.go internal/output/render_test.go
git commit -m "feat(output): renderers json e humano com portao de --no-redact"
```

---

### Task 5: Arquivo de configuração do ngx

**Files:**
- Create: `internal/settings/settings.go`
- Test: `internal/settings/settings_test.go`

**Interfaces:**
- Consumes: nada de tasks anteriores
- Produces: `settings.Settings` com `Nginx settings.Nginx` (campos `Binary`, `Config`) e `Output settings.Output` (campos `Format`, `Redact []string`); `settings.Load(globalPath, localPath string) (*Settings, error)`; `settings.Defaults() *Settings`

- [ ] **Step 1: Instalar as dependências**

```bash
go get github.com/knadh/koanf/v2@latest
go get github.com/knadh/koanf/providers/file@latest
go get github.com/knadh/koanf/parsers/yaml@latest
```

- [ ] **Step 2: Escrever o teste que falha**

Criar `internal/settings/settings_test.go`:

```go
package settings_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eduardoborges/ngx/internal/settings"
	"github.com/stretchr/testify/require"
)

func escreve(t *testing.T, dir, nome, conteudo string) string {
	t.Helper()
	p := filepath.Join(dir, nome)
	require.NoError(t, os.WriteFile(p, []byte(conteudo), 0o644))
	return p
}

// Sem nenhum arquivo, os defaults precisam ser utilizaveis por conta propria.
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

// A regra da spec: o local sobrescreve o global, chave a chave.
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
	require.Equal(t, "/usr/sbin/nginx", s.Nginx.Binary, "chave nao sobrescrita sobrevive")
	require.Equal(t, "json", s.Output.Format)
}

// Um arquivo escrito a partir da spec completa contem chaves de versoes
// futuras. Elas precisam ser ignoradas, nao virar erro.
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
// somar — senao ele nao consegue remover uma regra padrao.
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

- [ ] **Step 3: Rodar o teste para verificar que falha**

Run: `go test ./internal/settings/ -v`
Expected: FAIL — o pacote não existe.

- [ ] **Step 4: Escrever a implementação mínima**

Criar `internal/settings/settings.go`:

```go
// Package settings carrega o arquivo de configuracao do proprio ngx.
// A v0.1 le apenas o subconjunto que seus comandos usam; chaves de versoes
// futuras sao ignoradas sem erro, para que um arquivo escrito a partir da
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

// Nginx aponta para o binario e a configuracao principal.
type Nginx struct {
	Binary string `koanf:"binary"`
	Config string `koanf:"config"`
}

// Output controla formato e redacao.
type Output struct {
	Format string   `koanf:"format"`
	Redact []string `koanf:"redact"`
}

// Settings e a configuracao efetiva do ngx.
type Settings struct {
	Nginx  Nginx  `koanf:"nginx"`
	Output Output `koanf:"output"`
}

// Defaults devolve a configuracao usada quando nenhum arquivo existe. A
// redacao vem ligada: sem ela, um get pode vazar caminho de chave privada
// para dentro do contexto de um LLM rodando em API de terceiro.
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

// Load funde o arquivo global com o local, com o local vencendo chave a
// chave. Arquivo ausente nao e erro.
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

- [ ] **Step 5: Rodar os testes para verificar que passam**

Run: `go test ./internal/settings/ -v`
Expected: PASS — 6 testes.

> Se `TestRedactDeclaradoSubstituiODefault` falhar porque o koanf concatenou as listas em vez de substituir, a correção é limpar `s.Output.Redact` antes do `Unmarshal` quando `k.Exists("output.redact")` for verdadeiro, e só então restaurar o default se a chave não existir. Faça essa correção dentro deste passo; não altere o teste.

- [ ] **Step 6: Commit**

```bash
git add internal/settings/ go.mod go.sum
git commit -m "feat(settings): carregamento do arquivo de configuracao do ngx"
```

---

### Task 6: CLI root e tradução de exit code

**Files:**
- Create: `cmd/ngx/main.go`, `internal/cli/root.go`
- Test: `internal/cli/root_test.go`

**Interfaces:**
- Consumes: `output.Renderer`, `output.Format`, `output.CodeOf`, `output.New`, `output.Usage`, `output.NewRedactSet` (Tasks 1–4); `settings.Load`, `settings.Settings` (Task 5)
- Produces: `cli.GlobalFlags` com campos `ConfigPath`, `JSON`, `Human`, `Quiet`, `NoColor`, `NginxBin`, `NginxVersion`, `Timeout`, `Profile`, `NoRedact`; `cli.Context` com campos `Flags *GlobalFlags`, `Settings *settings.Settings`, `Renderer *output.Renderer`; `cli.Execute(args []string, stdout, stderr io.Writer, isTTY bool) output.ExitCode`; `cli.NewRoot(ctx *Context) *cobra.Command`

- [ ] **Step 1: Instalar o cobra**

```bash
go get github.com/spf13/cobra@latest
```

- [ ] **Step 2: Escrever o teste que falha**

Criar `internal/cli/root_test.go`:

```go
package cli_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/eduardoborges/ngx/internal/cli"
	"github.com/eduardoborges/ngx/internal/output"
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
// nao uma precedencia silenciosa.
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

// O erro precisa sair no envelope, no stdout, para o agente conseguir ler.
// Escrever so no stderr obrigaria o agente a capturar dois streams.
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

- [ ] **Step 3: Rodar o teste para verificar que falha**

Run: `go test ./internal/cli/ -v`
Expected: FAIL — o pacote não existe.

- [ ] **Step 4: Escrever a implementação mínima**

Criar `internal/cli/root.go`:

```go
// Package cli monta a arvore de comandos. Comandos produzem valores e erros
// tipados; a formatacao e o exit code sao responsabilidade de output.
package cli

import (
	"io"
	"time"

	"github.com/eduardoborges/ngx/internal/output"
	"github.com/eduardoborges/ngx/internal/settings"
	"github.com/spf13/cobra"
)

// Caminhos padrao do arquivo de configuracao do proprio ngx.
const (
	GlobalSettingsPath = "/etc/ngx/ngx.yaml"
	LocalSettingsPath  = ".ngx/config.yaml"
)

// GlobalFlags espelha as flags globais da spec.
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

// Context carrega o que todo comando precisa.
type Context struct {
	Flags    *GlobalFlags
	Settings *settings.Settings
	Renderer *output.Renderer
	Command  string
}

// Execute roda o CLI e devolve o exit code. Nunca chama os.Exit: isso e
// responsabilidade de main, o que mantem o CLI inteiro testavel.
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

	// Cobra devolve erro cru para flag e comando invalidos; tratamos como uso.
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
	// Um erro nunca e suprimido por --quiet nem bloqueado pelo portao de
	// --no-redact: o agente precisa saber o que deu errado.
	r.Quiet = false
	r.NoRedact = false
	_ = r.Render(env)
}

// NewRoot monta o comando raiz com as flags globais.
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
	p.StringVar(&f.Profile, "profile", "", "perfil do arquivo de configuracao do ngx")
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

Criar `internal/cli/errors.go`:

```go
package cli

import (
	"errors"

	"github.com/eduardoborges/ngx/internal/output"
)

func asNgxError(err error, target **output.Error) bool {
	return errors.As(err, target)
}

// comandoDe devolve o nome do comando que estava executando, para que o
// envelope de erro identifique a operacao que falhou. Antes de o cobra
// resolver o comando — flag global invalida, por exemplo — nao ha nome, e o
// fallback e o proprio binario.
func comandoDe(ctx *Context) string {
	if ctx == nil || ctx.Command == "" {
		return "ngx"
	}
	return ctx.Command
}
```

Criar `cmd/ngx/main.go`:

```go
// Command ngx e o ponto de entrada. A unica responsabilidade aqui e o wiring
// e a traducao do exit code.
package main

import (
	"os"

	"github.com/eduardoborges/ngx/internal/cli"
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

- [ ] **Step 5: Rodar os testes para verificar que passam**

Run: `go test ./... -v`
Expected: PASS em `internal/output`, `internal/settings` e `internal/cli`.

- [ ] **Step 6: Verificar o binário à mão**

```bash
go build -o /tmp/ngx ./cmd/ngx
/tmp/ngx version | head -1
/tmp/ngx --json --human version; echo "exit=$?"
```

Expected: a primeira linha é um envelope JSON com `"command":"version"`; a segunda imprime um envelope com `NGX-0002` e `exit=2`.

- [ ] **Step 7: Commit**

```bash
git add cmd/ internal/cli/ go.mod go.sum
git commit -m "feat(cli): comando raiz, flags globais e traducao de exit code"
```

---

### Task 7: A árvore e o parse via crossplane

**Files:**
- Create: `internal/config/node.go`, `internal/config/parse.go`
- Test: `internal/config/parse_test.go`, `internal/config/testdata/simples.conf`

**Interfaces:**
- Consumes: `output.RedactSet`, `output.Redactable`, `output.RedactedValue` (Task 3)
- Produces: `config.Span` (`Start`, `End`), `config.Origin` (`File`, `Line`), `config.Node` (campos conforme §4.1 da spec), `config.File` (`Path string`, `Source []byte`, `Nodes []*Node`), `config.Tree` (`Files []*File`, `Hash string`), `config.ParseOptions` (`Path string`, `Open func(string) (io.ReadCloser, error)`), `config.Parse(ParseOptions) (*Tree, error)`, `(*Node).IsComment() bool`, `(*Node).HasBlock() bool`, `(*Tree).Walk(func(*Node) bool)`

- [ ] **Step 1: Instalar o crossplane e criar a fixture**

```bash
go get github.com/nginxinc/nginx-go-crossplane@latest
```

Criar `internal/config/testdata/simples.conf`:

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

- [ ] **Step 2: Escrever o teste que falha**

Criar `internal/config/parse_test.go`:

```go
package config_test

import (
	"path/filepath"
	"testing"

	"github.com/eduardoborges/ngx/internal/config"
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
	require.NotEmpty(t, tree.Files[0].Source, "a fonte original precisa ser guardada para os spans")
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

// A redacao acontece na saida: a arvore em memoria mantem o valor real, senao
// fmt gravaria *** dentro do .conf do usuario.
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

- [ ] **Step 3: Rodar o teste para verificar que falha**

Run: `go test ./internal/config/ -v`
Expected: FAIL — o pacote não existe.

- [ ] **Step 4: Escrever o modelo de dados**

Criar `internal/config/node.go`:

```go
// Package config e a representacao canonica da configuracao do nginx: a
// arvore semantica vem do nginx-go-crossplane, os offsets de byte vem do
// tokenizador deste pacote, e as duas sao casadas por sequencia de tokens.
package config

// Span e um intervalo de bytes no arquivo de origem, com End exclusivo.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Len devolve o tamanho do intervalo em bytes.
func (s Span) Len() int { return s.End - s.Start }

// Origin registra de onde um no veio depois de resolver include.
type Origin struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Node e uma diretiva. Span cobre a diretiva inteira, incluindo o bloco e o
// delimitador final; HeadSpan cobre apenas o nome e os argumentos. Ter os
// dois e o que torna a edicao da v0.2 uma substituicao de bytes em vez de
// uma re-renderizacao do arquivo.
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

	// temBloco distingue "server {}" de "server;". O campo Block nao serve
	// para isso: um bloco vazio e uma slice vazia, indistinguivel de nil
	// depois da serializacao.
	temBloco bool
}

// IsComment informa se o no representa um comentario.
func (n *Node) IsComment() bool { return n.Directive == "#" }

// HasBlock informa se o no abre um bloco, inclusive vazio.
func (n *Node) HasBlock() bool { return n.temBloco }

// File e um arquivo de configuracao com sua fonte original preservada. A
// fonte e necessaria para que os spans possam ser resolvidos em texto.
type File struct {
	Path   string  `json:"file"`
	Source []byte  `json:"-"`
	Nodes  []*Node `json:"parsed"`
}

// Tree e o resultado completo de um parse.
type Tree struct {
	Files []*File `json:"config"`
	Hash  string  `json:"-"`
}

// Walk percorre a arvore em pre-ordem. Se fn devolver false, os filhos
// daquele no sao pulados.
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

- [ ] **Step 5: Escrever o parse**

Criar `internal/config/parse.go`:

```go
package config

import (
	"fmt"
	"io"
	"os"

	crossplane "github.com/nginxinc/nginx-go-crossplane"
)

// ParseOptions controla a leitura. Open existe para permitir testes com
// filesystem em memoria, sem tocar disco.
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

// Parse le a configuracao e devolve a arvore canonica. Cada arquivo e
// parseado separadamente, preservando sua fonte: a resolucao de include e
// uma view construida sobre esta arvore, nao uma concatenacao previa, para
// que os spans continuem apontando para offsets reais de arquivos reais.
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

- [ ] **Step 6: Rodar os testes para verificar que passam**

Run: `go test ./internal/config/ -v`
Expected: PASS — 6 testes.

> `temBloco` vem de `d.Block != nil`. Se o crossplane devolver uma slice vazia não-nil para blocos vazios e nil para diretivas simples, isso já está correto. Se devolver nil para os dois, `TestParseMonstaBlocosAninhados` ainda passa (os blocos do fixture não são vazios), mas o Task 9 corrigirá a detecção usando o próximo token. Não invente um teste de bloco vazio aqui.

- [ ] **Step 7: Commit**

```bash
git add internal/config/ go.mod go.sum
git commit -m "feat(config): arvore canonica e parse via crossplane"
```

---

### Task 8: Tokenizador com offsets de byte

**Files:**
- Create: `internal/config/tokens.go`
- Test: `internal/config/tokens_test.go`, `internal/config/fuzz_test.go`

**Interfaces:**
- Consumes: nada de tasks anteriores
- Produces: `config.TokenKind` com `TokenWord`/`TokenSemicolon`/`TokenBlockStart`/`TokenBlockEnd`/`TokenComment`; `config.Token` (`Kind`, `Value`, `Raw`, `Start`, `End`, `Line`, `Column`, `Quoted`); `config.Tokenize(src []byte) ([]Token, error)`

- [ ] **Step 1: Escrever o teste que falha**

Criar `internal/config/tokens_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"unicode"

	"github.com/eduardoborges/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

// A invariante que sustenta todo o resto: o texto entre Start e End precisa
// ser exatamente o Raw do token. Se isso vale, os spans sao confiaveis.
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

// Aspas escondem ; e { do tokenizador. Errar isso quebra o alinhamento
// inteiro no primeiro add_header com ponto e virgula dentro.
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

	// "listen" comeca na segunda linha, apos quatro espacos.
	require.Equal(t, "listen", toks[2].Value)
	require.Equal(t, 2, toks[2].Line)
	require.Equal(t, 5, toks[2].Column)
}

func TestAspasNaoFechadasVirarErro(t *testing.T) {
	_, err := config.Tokenize([]byte(`msg "sem fim;`))

	require.Error(t, err)
}

// Cobertura: todo byte que nao e espaco em branco pertence a algum token.
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
			"byte %d (%q) nao coberto e nao e espaco", i, string(b))
	}
}
```

Criar `internal/config/fuzz_test.go`:

```go
package config_test

import (
	"testing"

	"github.com/eduardoborges/ngx/internal/config"
)

// O fuzz garante que, para qualquer entrada que o tokenizador aceite, os
// spans continuam apontando para o texto real e em ordem crescente. E a rede
// que sustenta a edicao cirurgica da v0.2.
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
				t.Fatalf("span invalido [%d,%d) para fonte de %d bytes", tok.Start, tok.End, len(s))
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

- [ ] **Step 2: Rodar o teste para verificar que falha**

Run: `go test ./internal/config/ -run Token -v`
Expected: FAIL — `undefined: config.Tokenize`.

- [ ] **Step 3: Escrever a implementação mínima**

Criar `internal/config/tokens.go`:

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

// Token e um lexema com sua posicao exata em bytes. Value e o conteudo
// semantico (sem aspas, sem o # do comentario); Raw e o texto original.
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

// Tokenize quebra a fonte em tokens com offsets de byte. Nao interpreta
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
	return fmt.Errorf("aspa %q aberta na linha %d nao foi fechada", string(aspa), line)
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

- [ ] **Step 4: Rodar os testes para verificar que passam**

Run: `go test ./internal/config/ -v`
Expected: PASS — 9 testes de token mais os 6 de parse.

- [ ] **Step 5: Rodar o fuzz por 30 segundos**

Run: `go test ./internal/config/ -run FuzzTokenizeSpans -fuzz FuzzTokenizeSpans -fuzztime 30s`
Expected: `elapsed: 30s` sem falhas. Se o fuzz encontrar um caso, ele grava em `testdata/fuzz/`; corrija o tokenizador e mantenha o caso como regressão.

- [ ] **Step 6: Commit**

```bash
git add internal/config/tokens.go internal/config/tokens_test.go internal/config/fuzz_test.go
git commit -m "feat(config): tokenizador com offsets de byte"
```

---

### Task 9: Alinhamento token↔árvore

**Files:**
- Create: `internal/config/align.go`
- Modify: `internal/config/parse.go` — chamar o alinhamento ao final de `Parse`
- Test: `internal/config/align_test.go`

**Interfaces:**
- Consumes: `config.Node`, `config.File`, `config.Tree`, `config.Span` (Task 7); `config.Token`, `config.Tokenize` (Task 8)
- Produces: `config.alinhar(f *File) error` (não exportada; chamada por `Parse`). Após o Task 9, todo `*Node` devolvido por `Parse` tem `Span`, `HeadSpan`, `Line` e `Column` preenchidos, e `HasBlock()` reflete a presença real de `{`.

- [ ] **Step 1: Escrever o teste que falha**

Criar `internal/config/align_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardoborges/ngx/internal/config"
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
		"o head nao inclui o bloco")
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

// Aspas contendo ponto e virgula sao o caso que quebra um alinhamento ingenuo.
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

// Invariante de contencao: o span de um filho vive dentro do span do pai.
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

// Cobertura: todo byte significativo do arquivo pertence ao span de algum no
// de nivel raiz. E a formulacao concreta da propriedade que sustenta a
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
			"byte %d (%q) na linha nao coberta nao e espaco", i, string(b))
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

- [ ] **Step 2: Rodar o teste para verificar que falha**

Run: `go test ./internal/config/ -run "TestHeadSpan|TestSpan|TestLinha|TestAlinhamento|TestBloco" -v`
Expected: FAIL — os spans estão todos zerados.

- [ ] **Step 3: Escrever o alinhamento**

Criar `internal/config/align.go`:

```go
package config

import "fmt"

// alinhar casa a arvore semantica vinda do crossplane com os tokens do
// arquivo, anexando offsets de byte a cada no.
//
// O casamento e por sequencia: o crossplane preserva a ordem do documento, e
// com ParseComments ligado ate os comentarios sao nos da arvore. Entao um
// unico percurso simultaneo resolve tudo — nao ha busca nem heuristica.
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

	// Olhar o proximo token e mais confiavel que inspecionar n.Block: um
	// bloco vazio e indistinguivel de uma diretiva simples pelo campo Block.
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

- [ ] **Step 4: Chamar o alinhamento no parse**

Em `internal/config/parse.go`, dentro do laço sobre `payload.Config`, substituir o bloco que monta o `File` por:

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

- [ ] **Step 5: Rodar os testes para verificar que passam**

Run: `go test ./internal/config/ -v`
Expected: PASS — todos, incluindo os 8 novos de alinhamento.

- [ ] **Step 6: Rodar o fuzz do alinhamento**

Adicionar em `internal/config/fuzz_test.go`:

```go
// Alinhar nunca pode entrar em panico nem produzir span fora dos limites,
// qualquer que seja a entrada que o crossplane tenha aceitado.
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

		for _, arquivo := range tree.Files {
			n := len(arquivo.Source)
			tree.Walk(func(node *config.Node) bool {
				if node.Span.Start < 0 || node.Span.End > n || node.Span.Start > node.Span.End {
					t.Fatalf("span invalido [%d,%d) para fonte de %d bytes",
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

Adicionar os imports `os` e `path/filepath` ao arquivo de fuzz.

Run: `go test ./internal/config/ -run FuzzAlinhamento -fuzz FuzzAlinhamento -fuzztime 60s`
Expected: sem falhas. Casos encontrados ficam em `testdata/fuzz/` como regressão — commite-os.

- [ ] **Step 7: Commit**

```bash
git add internal/config/
git commit -m "feat(config): alinhamento token-arvore com spans de byte"
```

---

### Task 10: IDs estáveis

**Files:**
- Create: `internal/config/ids.go`
- Modify: `internal/config/parse.go` — atribuir IDs após o alinhamento
- Test: `internal/config/ids_test.go`

**Interfaces:**
- Consumes: `config.Node`, `config.Tree` (Task 7)
- Produces: `config.AtribuirIDs(nodes []*Node, prefixo string)`; `config.FindByID(t *Tree, id string) *Node`

- [ ] **Step 1: Escrever o teste que falha**

Criar `internal/config/ids_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eduardoborges/ngx/internal/config"
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

// Blocos de contexto do nivel raiz nao levam indice: ocorrem no maximo uma vez.
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

// A regra que reduz a fragilidade: o indice conta entre irmaos do mesmo tipo,
// nao por posicao absoluta. Inserir uma location nao renumera os servers.
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

// Comentarios nao recebem ID e nao contam no indice: se contassem, adicionar
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

	require.Empty(t, server.Block[0].ID, "comentario nao tem ID")
	require.Equal(t, "h.s0.d0", server.Block[1].ID)
	require.Empty(t, server.Block[2].ID)
	require.Equal(t, "h.s0.d1", server.Block[3].ID, "o comentario no meio nao deslocou o indice")
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

// Diretivas sem abreviacao na tabela usam o nome completo, o que mantem o ID
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

- [ ] **Step 2: Rodar o teste para verificar que falha**

Run: `go test ./internal/config/ -run "TestBlocos|TestServers|TestIndice|TestDiretivas|TestComentarios|TestLocations|TestDiretiva|TestFindByID" -v`
Expected: FAIL — `undefined: config.FindByID`, IDs vazios.

- [ ] **Step 3: Escrever a implementação mínima**

Criar `internal/config/ids.go`:

```go
package config

import (
	"fmt"
	"strings"
)

// abreviacoes encurta as diretivas de bloco mais comuns. Primeira letra
// sozinha nao serve: server e stream colidiriam.
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

// blocosRaiz sao os contextos de topo, que ocorrem no maximo uma vez e por
// isso dispensam indice: o ID e "h", nao "h0".
var blocosRaiz = map[string]bool{
	"http":   true,
	"stream": true,
	"events": true,
	"mail":   true,
}

// AtribuirIDs preenche o campo ID de cada no, recursivamente.
//
// O indice conta entre irmaos da mesma diretiva, nao por posicao absoluta:
// inserir uma location nao renumera os servers ao lado. Comentarios nao
// recebem ID nem participam da contagem, senao adicionar um comentario
// deslocaria os IDs das diretivas vizinhas.
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
	// apenas mais um bloco irmao e precisa ser numerado normalmente.
	if naRaiz && n.HasBlock() && blocosRaiz[n.Directive] {
		return abreviar(n.Directive)
	}

	chave := n.Directive
	base := abreviar(n.Directive)
	if !n.HasBlock() && abreviacoes[n.Directive] == "" {
		// Diretivas simples sem abreviacao propria compartilham o contador d.
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

// FindByID localiza um no pelo seu ID. Devolve nil se nao existir.
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

- [ ] **Step 4: Atribuir IDs no parse**

Em `internal/config/parse.go`, logo após `alinhar(arquivo)`:

```go
		AtribuirIDs(arquivo.Nodes, "")
```

- [ ] **Step 5: Rodar os testes para verificar que passam**

Run: `go test ./internal/config/ -v`
Expected: PASS — 9 testes novos de ID mais todos os anteriores.

- [ ] **Step 6: Commit**

```bash
git add internal/config/ids.go internal/config/ids_test.go internal/config/parse.go
git commit -m "feat(config): IDs estaveis contados entre irmaos do mesmo tipo"
```

---

### Task 11: Hash canônico da configuração

**Files:**
- Create: `internal/config/hash.go`
- Modify: `internal/config/parse.go` — calcular o hash ao final de `Parse`
- Test: `internal/config/hash_test.go`

**Interfaces:**
- Consumes: `config.Node`, `config.Tree` (Task 7)
- Produces: `config.Hash(t *Tree) string` (devolve `"sha256:<hex>"`)

- [ ] **Step 1: Escrever o teste que falha**

Criar `internal/config/hash_test.go`:

```go
package config_test

import (
	"strings"
	"testing"

	"github.com/eduardoborges/ngx/internal/config"
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

// O hash protege o significado, nao o texto: duas configuracoes que so diferem
// em formatacao precisam produzir o mesmo hash, senao rodar fmt invalidaria
// todos os IDs que o agente esta segurando.
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

// Ordem importa: mover um server muda o significado dos IDs, entao precisa
// mudar o hash.
func TestOrdemDeBlocosMudaOHash(t *testing.T) {
	a := parseTexto(t, "http { server { listen 80; } server { listen 443; } }")
	b := parseTexto(t, "http { server { listen 443; } server { listen 80; } }")

	require.NotEqual(t, a.Hash, b.Hash)
}

// Sem separador entre diretiva e argumentos, "a b" e "ab" colidiriam.
func TestDiretivasDiferentesNaoColidem(t *testing.T) {
	a := parseTexto(t, "ab c;")
	b := parseTexto(t, "a bc;")

	require.NotEqual(t, a.Hash, b.Hash)
}
```

- [ ] **Step 2: Rodar o teste para verificar que falha**

Run: `go test ./internal/config/ -run TestHash -v`
Expected: FAIL — `tree.Hash` vazio.

- [ ] **Step 3: Escrever a implementação mínima**

Criar `internal/config/hash.go`:

```go
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"strconv"
)

// Hash devolve o hash canonico da arvore.
//
// O que o hash protege e o significado, nao o texto: comentarios e espacamento
// ficam de fora, entao rodar fmt nao invalida os IDs que o agente esta
// segurando. Ja a ordem dos blocos entra, porque mover um server muda a que
// no cada ID se refere.
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

// escreverCampo usa um separador que nao pode aparecer numa diretiva, para
// que "ab c" e "a bc" nunca colidam.
func escreverCampo(h hash.Hash, s string) {
	_, _ = h.Write([]byte(s))
	_, _ = h.Write([]byte{0})
}
```

- [ ] **Step 4: Calcular o hash no parse**

Em `internal/config/parse.go`, antes do `return tree, nil`:

```go
	tree.Hash = Hash(tree)
```

- [ ] **Step 5: Rodar os testes para verificar que passam**

Run: `go test ./internal/config/ -v`
Expected: PASS.

> `TestFormatacaoDiferenteProduzMesmoHash` e `TestComentariosNaoEntramNoHash` usam arquivos em `t.TempDir()` diferentes, e o caminho do arquivo entra no hash via `escreverCampo(h, f.Path)`. Isso fará os dois testes falharem. A correção é usar apenas o **nome base** do arquivo no hash, não o caminho absoluto — o que também é o comportamento certo: mover a configuração de diretório não muda seu significado. Aplique essa correção neste passo; não altere os testes.

- [ ] **Step 6: Commit**

```bash
git add internal/config/hash.go internal/config/hash_test.go internal/config/parse.go
git commit -m "feat(config): hash canonico ancorando os IDs"
```

---

### Task 12: Resolução de include com rastreio de origem

**Files:**
- Create: `internal/config/combine.go`
- Test: `internal/config/combine_test.go`, `internal/config/testdata/combine/nginx.conf`, `internal/config/testdata/combine/conf.d/api.conf`

**Interfaces:**
- Consumes: `config.Tree`, `config.File`, `config.Node`, `config.Origin`, `config.AtribuirIDs` (Tasks 7, 10)
- Produces: `config.Combine(t *Tree) (*Tree, error)` — devolve uma nova árvore com um único `File`, onde cada `include` foi substituído pelos nós dos arquivos incluídos e cada nó carrega `Origin`

- [ ] **Step 1: Criar as fixtures**

Criar `internal/config/testdata/combine/nginx.conf`:

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

Criar `internal/config/testdata/combine/conf.d/api.conf`:

```nginx
server {
    listen 443 ssl;
    server_name api.exemplo.com;
}
```

- [ ] **Step 2: Escrever o teste que falha**

Criar `internal/config/combine_test.go`:

```go
package config_test

import (
	"path/filepath"
	"testing"

	"github.com/eduardoborges/ngx/internal/config"
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
		"o include sumiu e virou o server do arquivo incluido")
}

// Origin e o que permite ao agente saber em qual arquivo real editar depois
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

// Os IDs da arvore combinada sao renumerados sobre a estrutura resolvida:
// e essa a estrutura que o agente enxerga e sobre a qual ele opera.
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

// O hash da arvore combinada difere do da nao-combinada: sao visoes
// diferentes, e confundi-las invalidaria IDs sem motivo.
func TestCombineRecalculaOHash(t *testing.T) {
	original := parseCombine(t)
	combinado, err := config.Combine(original)
	require.NoError(t, err)

	require.NotEmpty(t, combinado.Hash)
	require.NotEqual(t, original.Hash, combinado.Hash)
}
```

- [ ] **Step 3: Rodar o teste para verificar que falha**

Run: `go test ./internal/config/ -run Combine -v`
Expected: FAIL — `undefined: config.Combine`.

- [ ] **Step 4: Escrever a implementação mínima**

Criar `internal/config/combine.go`:

```go
package config

import "fmt"

// Combine resolve os includes, devolvendo uma arvore de arquivo unico onde
// cada no carrega a origem real.
//
// A resolucao e feita sobre a nossa arvore, e nao pelo CombineConfigs do
// crossplane, porque combinar antes destruiria os spans: eles apontam para
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

// arquivos e uma slice, e nao um map, de proposito: um include com glob pode
// casar varios arquivos, e iterar um map daria ordem diferente a cada
// execucao — o que faria os IDs e o hash mudarem sem a configuracao mudar.
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

// expandirInclude localiza os arquivos que casam com o padrao do include.
// O crossplane ja resolveu os globs e devolveu cada arquivo casado como um
// Config proprio, entao basta encontrar os que ainda nao foram consumidos.
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

// A iteracao e sobre a slice de arquivos, na ordem em que o crossplane os
// devolveu, para que o resultado seja deterministico.
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

Criar também, no mesmo arquivo, o casamento de caminho:

```go
// casaInclude decide se um arquivo parseado corresponde ao padrao de um
// include. O padrao pode ser relativo ao arquivo que o declarou.
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

Adicionar `"path/filepath"` aos imports.

- [ ] **Step 5: Rodar os testes para verificar que passam**

Run: `go test ./internal/config/ -v`
Expected: PASS — 7 testes de combine mais todos os anteriores.

> `TestCombineRenumeraIDsSobreAEstruturaResolvida` exige que o `server` do arquivo incluído venha **antes** do `server` declarado em `nginx.conf`, porque o `include` aparece antes. Se falhar, o problema é a ordem em `expandir`, não o teste.

- [ ] **Step 6: Commit**

```bash
git add internal/config/combine.go internal/config/combine_test.go internal/config/testdata/combine/
git commit -m "feat(config): resolucao de include com rastreio de origem"
```

---

### Task 13: Comando `inspect`

**Files:**
- Create: `internal/cli/inspect.go`
- Modify: `internal/cli/root.go` — registrar o comando
- Test: `internal/cli/inspect_test.go`, `internal/cli/testdata/exemplo.conf`

**Interfaces:**
- Consumes: `cli.Context`, `cli.NewRoot` (Task 6); `config.Parse`, `config.Combine`, `config.Tree` (Tasks 7–12); `output.New`, `output.Usage`, `output.Internal`, `output.RedactSet`, `output.RedactedValue` (Tasks 1–4)
- Produces: `cli.InspectData` (`Config []*config.File`, `Summary cli.Summary`); `cli.Summary` (`Files`, `Servers`, `Locations`, `Upstreams int`); método `(InspectData).Redacted(output.RedactSet) any`

- [ ] **Step 1: Criar a fixture**

Criar `internal/cli/testdata/exemplo.conf`:

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

- [ ] **Step 2: Escrever o teste que falha**

Criar `internal/cli/inspect_test.go`:

```go
package cli_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/eduardoborges/ngx/internal/cli"
	"github.com/eduardoborges/ngx/internal/output"
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

// O hash no meta e a ancora dos IDs que saem no data.
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

// Os IDs precisam sair no JSON: e por eles que o agente referencia um no na
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

// O teste que fecha o ciclo da redacao: o valor sensivel nao pode aparecer na
// saida, mas a diretiva sim.
func TestInspectRedigeChavePrivada(t *testing.T) {
	_, _, bruto := rodarInspect(t, "inspect", "-c", fixture(t))

	require.NotContains(t, bruto, "/etc/ssl/private/api.key")
	require.Contains(t, bruto, "ssl_certificate_key", "a diretiva continua visivel")
	require.Contains(t, bruto, output.RedactedValue)
}

// Arquivo inexistente e falha de IO, nao erro de uso: a flag estava correta,
// o disco e que nao tinha o arquivo.
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
		"o include foi resolvido e nao aparece mais na arvore")
}
```

- [ ] **Step 3: Rodar o teste para verificar que falha**

Run: `go test ./internal/cli/ -run Inspect -v`
Expected: FAIL — comando `inspect` desconhecido.

- [ ] **Step 4: Escrever a implementação mínima**

Criar `internal/cli/inspect.go`:

```go
package cli

import (
	"github.com/eduardoborges/ngx/internal/config"
	"github.com/eduardoborges/ngx/internal/output"
	"github.com/spf13/cobra"
)

// Summary e a visao de uma linha da configuracao. Existe para o agente saber
// o tamanho do que esta olhando sem ter que contar nos.
type Summary struct {
	Files     int `json:"files"`
	Servers   int `json:"servers"`
	Locations int `json:"locations"`
	Upstreams int `json:"upstreams"`
}

// InspectData e o dump completo: arvore mais resumo.
type InspectData struct {
	Config  []*config.File `json:"config"`
	Summary Summary        `json:"summary"`
}

// Redacted devolve uma copia com os valores sensiveis substituidos. A copia e
// profunda nos nos afetados: a arvore original nunca e alterada, senao um fmt
// posterior gravaria *** no arquivo do usuario.
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

Em `internal/cli/root.go`, registrar o comando junto do `version`:

```go
	root.AddCommand(newVersionCmd(ctx))
	root.AddCommand(newInspectCmd(ctx))
```

- [ ] **Step 5: Rodar os testes para verificar que passam**

Run: `go test ./... -v`
Expected: PASS em todos os pacotes.

> `TestInspectResumeAConfiguracao` conta `server` como diretiva. A fixture tem `server 10.0.0.1:8080;` **dentro** do upstream, que também se chama `server`. Se o teste contar 2 servers, a correção é contar apenas `server` que abre bloco (`n.HasBlock()`), o que também é o comportamento correto — `server` dentro de `upstream` é outra diretiva. Aplique a correção; não altere o teste.

- [ ] **Step 6: Verificar o binário à mão**

```bash
go build -o /tmp/ngx ./cmd/ngx
/tmp/ngx inspect -c internal/cli/testdata/exemplo.conf | head -c 400; echo
/tmp/ngx inspect -c internal/cli/testdata/exemplo.conf | grep -c 'private/api.key'
```

Expected: um envelope JSON com a árvore; o `grep -c` imprime `0`, confirmando que a chave privada não vazou.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/ 
git commit -m "feat(cli): comando inspect"
```

---

### Task 14: README, vet e verificação final do plano

**Files:**
- Create: `README.md`, `Makefile`
- Test: nenhum novo; roda a suíte inteira

**Interfaces:**
- Consumes: tudo
- Produces: `make test`, `make fuzz`, `make build`

- [ ] **Step 1: Criar o Makefile**

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

- [ ] **Step 2: Criar o README**

Criar `README.md`:

````markdown
# ngx

Um CLI em Go que torna o nginx operável por agentes de IA com segurança.

Hoje um agente que precisa mexer em nginx lê `.conf` como texto solto, edita com
substituição de string e descobre que errou quando o `nginx -t` falha — ou pior,
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
$ make build      # compila em bin/ngx
$ make test       # suíte completa com race detector
$ make fuzz       # fuzzers do tokenizador e do alinhamento
```

Requer Go 1.25. O `.tool-versions` fixa a versão para quem usa asdf.

## Design

As decisões de arquitetura e o porquê de cada uma estão em
[`docs/superpowers/specs/`](docs/superpowers/specs/).

## Licença

MIT. Copyright (c) 2026 Eduardo Benck.
````

- [ ] **Step 3: Rodar a suíte completa com race detector**

Run: `make vet && make test`
Expected: PASS em todos os pacotes, sem avisos do vet.

- [ ] **Step 4: Rodar os fuzzers**

Run: `make fuzz`
Expected: sem falhas. Casos novos em `testdata/fuzz/` devem ser commitados como regressão.

- [ ] **Step 5: Commit**

```bash
git add README.md Makefile
git commit -m "chore: makefile e readme"
```

---

## Verificação de cobertura da spec

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
| §8.1 arquivo de configuração | 5 |
| §9 property test de spans | 8, 9 |
| §9 fuzzing | 8, 9 |
| §9 golden files, fake nginx, integração | **Plano 3** |
| §10 repositório, licença | 1, 14 |
| §10 CI e goreleaser | **Plano 3** |

**Refinamento de §9 da spec:** a spec descreve a propriedade dos spans como "reconstituem o arquivo byte a byte". A formulação concreta e verificável adotada aqui é mais forte e está em `TestSpansRaizCobremTodoByteSignificativo` mais `TestSpansDeFilhosEstaoContidosNoPai`: todo byte não-branco pertence ao span de algum nó raiz, spans de filhos estão contidos nos dos pais, e irmãos não se sobrepõem. Vale atualizar a spec para essa redação quando o Plano 1 fechar.
