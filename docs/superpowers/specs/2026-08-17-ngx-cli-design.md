# ngx v0.1 — Design

Based on: `ngx` v1.0 technical specDate: 2026-08-17
Status: approved, ready for implementation plan

---

## 1. Scope

This document projects `ngx` **v0.1**: the foundation and reading commands.
Nothing in this release changes the configuration of a running server.

Enter v0.1:

| Area | Delivery |
|---|---|
| Foundation | JSON envelope, exit codes, ngx config loading, writing |
| Parsee | Canonical tree with stable IDs, byte spans, `include` resolution |
| Selectors | complete reading language (`get`) |
| Runtime | nginx detection, structured `nginx -t`, drift signal |
| Commands | `status`, `inspect`, `get`, `tree`, `fmt`, `test`, `diff` |

They are outside of v0.1, by version: mutation and transaction (v0.2), `lint` (v0.3),
`route` (v0.4), MCP (v0.5), `logs` and `upstreams` (v0.6).

v0.1 is deliberately read-only. The two riskiest bets in
project — the selector language and the stability of IDs — are validated
before there is any code path capable of writing to a `.conf`
production.

---

## 2. Decisions

premises for everything that comes after.Decisions made during brainstorming, with the reason for each one. They are

### D1 — Surgical preservation of formatting

When `ngx` rewrites a `.conf`, comments, spacing and author style
original remains byte by byte, except in the part actually changed.

*Why:* the tool edits files that humans maintain. An `apply` that
reformats the entire file produces an unreadable diff, hides the real change
within the noise and destroys the trust that the spec tries to build with the summary of
impact. An agent who reformats a colleague's file is an agent that no one
authorizes it to run again.

*Consequence:* the tree needs to carry byte offsets. See D2.

### D2 — Own spans on the crossplane

`nginx-go-crossplane` provides the semantic tree and directive validation.
An independent tokenizer of ours provides the byte offsets. The two
Structures are matched by sequence of tokens.

*Why:* Crossplane solves edge cases that would take months to
reimplement — quoting, escapes, `map`, Lua blocks, module directives — and
validates context and directive arity for free. But your `Directive` loads
just `Line`, and even the lexer's `NgxToken` (`{Value, Line, IsQuoted, Error}`)
It has no offset or column. Neither D1 nor the `column` field required throughout
diagnostics come out of the pure crossplane.

*Alternatives discarded:* vendored fork of crossplane (less immediate effort,
but indefinite fork maintenance in a one-person project); parser 100%
own (throws away years of already resolved edge cases, against the non-objective
to reimplement what works).

*Risk and mitigation:* the token↔tree marriage is the fragile part. It is covered by a
property test that underpins the entire architecture (§9). If the property test
prove impossible to satisfy, the contingency plan is to propose offsets
upstream in the crossplane.

### D3 — Hash-anchored positional IDs

ID presented with a different hash is rejected with exit 9.IDs are derived from structural position, counted between siblings of the same type
directive**. Every envelope that returns IDs carries `config_hash` in `meta`. One

The hash anchor converts a
silent error — the agent edits the wrong node — in an explicit error. *Why:* the v1.0 spec promises that the agent can reference a node between
It's the principle
calls without rereading everything, but purely positional IDs change meaning
"ambiguity is error, don't guess" applied to time rather than space, and reuses the
when a previous sibling is inserted or removed. optimistic locking mechanism that the spec already defines for patches.

### D4 — Drift by evidence, not by master hash

`drift` is derived from comparing the mtime of the configuration files and the
start time of the master process. `config_loaded_hash` is only reported when
`ngx` itself performed the reload and recorded the applied hash. A field
`drift_evidence` tells you which source responded.

*Why:* the v1.0 spec assumes that `config_loaded_hash` is obtainable, and it is not.
`nginx -T` reads from disk — dumps the configuration that the binary would now load,
not the one the master has in memory. Implemented as the spec describes, the field
would always be identical to the disk hash and `drift` would be constant `false`:
field that the spec calls "gold for an agent" would lie in all cases. O
nginx OSS does not expose the configuration in memory by any flags or signals.

*Trade-off accepted:* the mtime signal knows something has changed, not what. In
Compensation works in the case that matters most — a human edited the file, not
reloaded — which the exact source doesn't cover.

### D5 — Writing in the renderer

The writing of sensitive values happens in the serialization of the output, never in the
tree in memory.

*Why:* if the tree were written in parse, `fmt` would write `***` into the
user `.conf`. The writing exists to protect what comes out into the context of
an LLM, not to mutilate the internal data.

### D6 — Personal project, open source, MIT

release packaging assembled since v0.1.Author's personal repository, MIT license, no institutional affiliation. IC and

---

## 3. Architecture

```
cmd/ngx/main.go          wiring and error-to-exit-code translation
internal/
  cli/       cobra: root, global flags, the 7 commands
  output/    envelope, renderers json|human, redaction, exit codes
  config/    parse · spans · ids · combine · render · hash
  selector/  lexer · parser · eval
  runtime/   detect (-V) · test (-t) · dump (-T) · exec · process
  drift/     disk vs. loaded comparison
  settings/  ngx configuration file (koanf)
```

Future release packages — `plan`, `patch`, `snapshot`, `lint`, `route`,
`mcp`, `logs` — are not created now. Empty directory is debt, no
architecture.

What ensures that they fit later is the `config` boundary: it returns a
immutable tree, complete, with spans and IDs. Every future consumer is a reader
of this tree. None of them reopen files, reparse text or reimplement
resolution of `include`.

**Layer rule:** `cli/` doesn't format anything and `output/` doesn't decide anything. One
command produces a typed value and, on failure, a typed error that carries
your exit code. `output/` turns this into JSON, human text, and code
exit. This is what prevents the envelope from becoming `fmt.Println` spread across seven
files and the exit code table from differing between commands.

---

## 4. Data Model

### 4.1 Node

```go
type Span struct {
    Start int // byte offset, inclusive
    End   int // byte offset, exclusive
}

type Origin struct {
    File string
    Line int
}

type Node struct {
    Directive string
    Args      []string
    File      string
    Line      int
    Column    int
    Span      Span    // from the directive's first letter to the closing ';' or '}'
    HeadSpan  Span    // directive + args only, without the block
    ID        string
    Comment   *string
    Block     []*Node
    Origin    *Origin // filled in when running with --combine
}
```

Two spans and not one: `Span` is the range that a removal erases; `HeadSpan` is the
range that an argument substitution rewrites. Have both since v0.1
is what makes the v0.2 edit a byte substitution rather than a
re-rendering the file.

are `#` directive nodes.`Comment` is populated by the crossplane with `ParseComments: true`; comments

### 4.2 ID generation

An ID is a sequence of segments separated by `.`. Each segment is
`<abbreviation><index>`, where the index is the position between the siblings **of the same
directive**, base 0.

Abbreviation table:

| Directive | Abbrev |
|---|---|
| `http` | `h` |
| `stream` | `st` |
| `events` | `e` |
| `mail` | `m` |
| `server` | `s` |
| `location` | `l` |
| `upstream` | `u` |
| `map` | `mp` |
| any other | full name of the directive |

Simple (non-block) directives use `d<N>` counted among the simple directives
sisters. The root-level context blocks — `http`, `events`, `mail`, `stream`
— omit the index, as they occur at most once: the ID is `h`, not `h0`.

Examples: `h.s0`, `h.s0.d1`, `h.s1.l2`, `h.s1.l2.l0`, `h.u0`.

Counting among siblings of the same type, and not by absolute position, means that
adding a `location` does not renumber the `server` next to it — it degrades the
fragility without eliminating it. The elimination comes from the hash anchor (D3).

### 4.3 Configuration hash

`config_hash` is `sha256` of the normalized tree in combine mode: directives and
canonically serialized arguments, comments and spacing excluded. Two
configurations that only differ in formatting produce the same hash — which is
correct, because what the hash protects is the meaning, not the text.

---

## 5. Selector language

The grammar is that of §5 of the v1.0 spec. This document sets out the four rules of
disambiguation that the grammar leaves open and that only appear when implementing the
lexer.

### R1 — The `.` is a separator only outside of square brackets

with `'` or `"`, which resolves regex locations and values containing `,` or `]`:`http.server[server_name=api.example.com]` — dots within the value do not
separate segment. The lexer maintains depth of `[`. Values can be quoted

```
location["~ \.php$"]
```

### R2 — `#` at the beginning is ID; in any other position it is index

`#h.s1.l2` is an ID literal. `upstream#2` is the third `upstream` (base 0,
as per the example in the spec). The distinction is positional: `#` as first
integer selector character selects by ID.

### R3 — Predicate about the node itself vs. about a son

The spec examples mix the two cases. The rule:

| Shape | Meaning |
|---|---|
| `[/api]` | sugar for `arg0=/api` — node's own argument |
| `[arg0=/api]`, `[arg1=ssl]` | argument of the node itself, explicit |
| `[server_name=api.com]` | **child** directive `server_name` with some arg matching |

directive name.There is no ambiguity because `argN` is reserved; any other key can only be

### R4 — Quantification over multiple arguments

`server_name a.com b.com` has multiple arguments. `=`, `~` and `^=` match each other
**some** argument satisfies the predicate. `!=` matches if **no** arguments
satisfy. The inversion of the quantifier in negation is explicit because leaving it
implicit is a predictable source of bug.

### Operators

| Op | Semantics |
|---|---|
| `=` | exact equality |
| `~` | contains |
| `^=` | prefix |
| `!=` | no arguments are the same |

Multiple predicates within a filter are conjunction (logical AND).

---

## 6. Envelope, exit codes and writing

### 6.0 Diagnostic code: `NGX-NNNN`, always numeric

The `code` of a `Diagnostic` is **public interface**: an agent consuming the output branches through it. Therefore, the format is fixed, `NGX-` followed by four digits, and the allocation is by range:

| Range | Domain |
|---|---|
| `0001`–`0009` | generic, aligned to the exit code of the same number |
| `0100`–`0199` | configuration and parse |
| `0200`–`0299` | transport and SSH |
| `0300`–`0399` | update and distribution |

**The severity is never included in the code.** `Diagnostic` already has the `severity` field; repeating the information as a prefix (`NGX-W001`, `NGX-E001`) creates two sources of truth that may disagree, and forces the consumer to *parse* the string to discover something that is already structured.

This rule exists because its absence was costly: two subagents working in parallel on different files each invented their own family — `NGX-W00N` and `NGX-E00N` — because neither owned the namespace and nothing said what the scheme was. Code is not implementation detail of a package; It's a product contract, and a contract needs an owner.

*Known limitation, unresolved:* codes `0001`–`0009` identify the **exit code**, not the condition. Any invalid configuration comes out as `NGX-0003`, whatever the cause. In `internal/config` the specific condition lives in a separate field, `Class`. There are two identity mechanisms for the same purpose, and unifying them is an open decision — the likely path is for the `Class` to become code in the `0100` range, and the `0003` to remain just a generic for those who do not have their own range.

### 6.1 Envelope

Structure identical to §6 of spec v1.0:

```go
type Envelope struct {
    OK            bool         `json:"ok"`
    Command       string       `json:"command"`
    SchemaVersion int          `json:"schema_version"`
    NgxVersion    string       `json:"ngx_version"`
    Data          any          `json:"data"`
    Diagnostics   []Diagnostic `json:"diagnostics"`
    Meta          Meta         `json:"meta"`
}
```

`schema_version` (added in v0.1.1) versions the SHAPE, and it is what a consumer
branches on. A plain integer, not semver: nothing to parse wrong. It increments
only on a change that breaks whoever reads the output — a field renamed or
removed, a type changed, the meaning of a field changed — and never on a field
being added. `ngx_version` cannot serve: it moves on every release, including
the ones that leave the shape untouched. It goes out in every envelope, error
ones included.

`Meta` from v0.1 loads `duration_ms`, `nginx_version` and `config_hash` (D3).

`Diagnostic` loads `severity`, `code`, `message`, `file`, `line`, `column`,
`selector` and `docs`. The `fix` field exists in the struct but remains empty in v0.1,
as no commands in this version produce patches.

JSON is the default when stdout is not TTY; `--human` and `--json` force.

### 6.2 Exit codes

v0.1 only emits the codes that its commands can produce:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | internal error/IO |
| 2 | usage error (invalid flag, malformed selector) |
| 3 | invalid configuration (`nginx -t` failed) |
| 7 | drift detected |
| 9 | `config_hash` diverges from the ID supplied |

Codes 4, 5, 6 and 8 belong to commands that do not yet exist and are not
documented as supported until they are issueable. Exit code
documented but never issued is worse than absent: an agent writes treatment
for a case that never occurs and fails to deal with what happens.

single dot in `main.go` translates.Commands do not choose exit code. Each error is a type that carries its own, and a

### 6.3 Redaction

Configured in `output.redact`. The three formats that the spec uses as an example are
unified in a single matcher — directive name with argument prefix
optional:

```yaml
redact:
  - ssl_certificate_key                 # by directive name
  - proxy_set_header Authorization      # name + argument prefix
  - "**.auth_basic_user_file"           # context prefix, accepted and redundant
```

The `**.` prefix is accepted for compatibility with the spec, but is redundant:
matchers are valid in any context. Accepting it prevents a configuration
writing from the spec fails silently.

Behavior:

- The **value** is replaced by `***`; the directive, `id` and line remain.
  Removing the entire node would cause the agent to conclude that the policy does not exist, which
  It's worse than hiding the value.
- The node carries `redacted_args` (added in v0.1.1), the indices of the
  arguments that were replaced. A configuration may contain `***` of its own,
  and without the list the consumer cannot tell censorship from content — it
  retries in a loop or reports the key as empty. The field is omitted when
  nothing was redacted, and only the arguments AFTER the rule's matched prefix
  are replaced: the prefix is text the user wrote in `output.redact`, and
  keeping it visible is what says WHICH header was censored. Like the `***`
  itself, the mark is written on the render copy, never on the tree (D5).
- `diff` passes through the redaction like any other output. It's the easiest point to
  leak without realizing it.
- `fmt` writing to disk **doesn't** redact (D5).
- `--no-redact` is accepted only when stdout is TTY. A human debugging sees the
  secret; an agent reading the pipe, structurally, cannot. It costs few
  lines and closes the hole that the writing exists to close.

---

## 7. Runtime

string is interpolated in shell.All nginx invocations use `exec.Command` with explicit argv. None

### 7.1 Detection

`nginx -V` writes to **stderr**. `prefix` (`--prefix=`) are extracted from it,
`main_config` (`--conf-path=`), the pidfile path (`--pid-path=`), the version and
static modules (`--with-*_module`).

**Dynamic** modules loaded via `load_module` do not appear in `-V`. The list
of modules is completed from the tree, otherwise `modules` is incomplete
exactly in non-trivial cases.

### 7.2 Process status

- `running` and `master_pid`: reading the pidfile and signal 0. Portable, without
  new dependency.
- `workers` and `config_loaded_at`: require process inspection, which differs between
  Linux and Darwin. **Unavailable field is omitted from JSON, never estimated.** A
  agent treats the absence of a field much better than a wrong number.

### 7.3 structured `nginx -t`

The error output has the form:

```
nginx: [emerg] unknown directive "foo" in /etc/nginx/conf.d/a.conf:3
nginx: configuration file /etc/nginx/nginx.conf test failed
```

The parser converts each line into a `Diagnostic` and, using the tree already
loaded, **translates `file:line` back to a `selector` and an `id`**.

This accomplishes item 1 of Appendix B of the spec: the agent receives the error from itself
nginx already addressed in the language with which it operates the tool, without reparsing
nothing. It's almost free because the spans already exist.

### 7.4 Drift

According to D4:

```json
{
  "drift": true,
  "drift_evidence": "mtime",
  "config_on_disk_hash": "sha256:ab12...",
  "config_loaded_hash": null
}
```

`drift_evidence` takes `mtime` (files modified after master starts)
or `hash` (`ngx` recorded the reload and compared contents). When no source
can respond — master is not running, for example — `drift` is `null`, and
not `false`.

Drift comparison is never textual: `nginx -T` does not match byte for byte with
disk. When there is content comparison, it is between normalized trees.

---

## 8. v0.1 commands

`--profile`.Global flags as per §4.1 of the spec: `--config/-c`, `--json`, `--human`,
`--quiet/-q`, `--no-color`, `--nginx-bin`, `--nginx-version`, `--timeout`,

| Command | Contract | Exit |
|---|---|---|
| `status` | runtime state + drift | 0, or 7 if drift |
| `inspect` | runtime + full tree + summary | 0, 3 |
| `get <selector>` | subset of the tree; mandatory selector | 0, 2 if malformed, 9 if hash divergent |
| `tree` | summarized server/location/upstream hierarchy with IDs | 0 |
| `fmt` | format; `--check` does not write, `--write` writes | 0, or 7 with `--check` if there is a difference |
| `test` | structured wrapper of `nginx -t` | 0, 3 |
| `diff` | textual drift and/or what `fmt` would change | 0, or 7 if there is a difference |

`get` without selector is a usage error, not a total dump — consistent with the principle of
context economy, and anticipates the restriction that the MCP will make mandatory.

`fmt` is the only v0.1 command that writes to disk, and only with `--write`
explicit. Writing is atomic: temporary file on the same filesystem, `fsync`,
`rename`, with permissions preservation.

About `diff` in v0.1: the spec lists `diff` in the v0.1 roadmap but describes it in
§4 as transaction command ("textual diff of what would change"). No mutation in this
version there is no "what would change". The adopted reading is the one that makes sense without
writing: drift diff and formatting diff.

### 8.1 Configuration file

v0.1 loads, via koanf, the subset of §13 that its commands use:
`nginx.binary`, `nginx.config`, `output.format` and `output.redact`. The location
(`./.ngx/config.yaml`) overrides the global (`/etc/ngx/ngx.yaml`). Keys
future versions are ignored without error, so a file written from the
full spec works today.

---

## 9. Tests

Test-driven development, from the first commit.

If this property holds, the token↔tree marriage of D2 is
justifies choosing the D2 approach instead of the fork.correct and the surgical edition of v0.2 is safe. **Property test that supports the architecture.** For any `.conf` in the corpus:
If it breaks, it breaks loud and early,
the spans of all nodes, plus the gaps between them, reconstitute the file
before there was any writeable code in production. **byte by byte**. It is the test that

**Fuzzing.** In the lexer and selector parser, and in the token↔tree alignment.

**Golden files.** Corpus of real `.conf`, including those from the test repository
of the crossplane, serialized in JSON with tree, spans and IDs. `-update` flag for
regeneration.

**Fake nginx.** A Go binary compiled by the test itself, with scenarios
driven by environment variable. Practice parsing `-V`, `-t` and `-T`,
including the error paths, without Docker and without shell.

**Integration.** Container with real nginx, under `//go:build integration`, outside of
`go test` pattern. Validates that error detection and parsing work against the
real binary, and not just against the fake.

---

## 10. Repository and distribution

- Go module: `github.com/s0beran0/ngx` — **to be confirmed** before the first
  push; derived from the local path, not the actual GitHub handle.
- Fixed toolchain in `.tool-versions`: `golang 1.25.9`.
- MIT License, in the author's personal name.
- CI on GitHub Actions: build, `go vet`, tests with race detector, lint.
- Release with goreleaser: cross-compile for linux/amd64, linux/arm64 and darwin.
  Zero CGO, static single binary.

Distribution, release channels and self-update have their own plan in
`docs/superpowers/plans/2026-08-17-ngx-distribution.md`, with three decisions that
extend this section: semver-derived channels (clear tag is stable, suffix
`-beta`/`-rc` is pre-release), release check by SHA256 checksum more
minisign signature, and public key embedded in the binary at compile time.
The `ngx update` command was not included in §4 and now exists.

The reason for the signature, and not just the checksum: `ngx` runs as root in
servers that serve traffic. An auto-update verified only by checksum accepts
any binary that can publish a release, because the attacker publishes
his checksum together. The subscription maintains the guarantee even with the
GitHub compromised.

Remote access via SSH has its own plan in
`docs/superpowers/plans/2026-08-17-ngx-remote-ssh.md`, anticipating part of the
"multi-host via SSH" which §16 puts in v1.0. It operates without installing anything on the
server: reads the configuration via SFTP and runs the nginx that already exists there. Three
decisions: strict host key checking with explicit escaping, authentication
trying `ssh-agent` before any key files, and `~/.ssh/config`
respected so that `ngx --host web1` works for those who already have `ssh web1`.

This plan also fixes a defect in v0.1 that only becomes visible in use
remote: `ngx` injects `Open` into the crossplane but not `Glob`, so
`include conf.d/*.conf` is resolved with `filepath.Glob` on the local disk.
Pointed at a remote host, `ngx` would list files from the operator's machine
and would treat them as server configuration.

v0.1 dependencies:

| Use | Package |
|---|---|
| configuration parse | `github.com/nginxinc/nginx-go-crossplane` |
| CLI | `github.com/spf13/cobra` |
| ngx configuration | `github.com/knadh/koanf` |
| diff | `github.com/hexops/gotextdiff` |
| tests | `github.com/stretchr/testify` |

---

## 11. Divergences in relation to spec v1.0

Registered so that the spec can be updated.

| # | Point | Divergence |
|---|---|---|
| 1 | §3.2 `config_loaded_hash` | Not obtainable: `nginx -T` reads from disk, not from master memory. Replaced by evidence model (D4). |
| 2 | §3.1 IDs | Anchored in `config_hash`, with explicit rejection when the hash diverges (D3). |
| 3 | §5 grammar | Four ambiguities resolved by explicit rule (R1–R4). |
| 4 | §6 exit codes | v0.1 only documents the codes it issues. |
| 5 | §13 `redact` | The three formats unified in a single matcher; `**.` accepted as redundant. |
| 6 | §14 writing | Added: `--no-redact` refused when stdout is not TTY. |
| 7 | v0.1 roadmap | `diff` reinterpreted as drift + formatting, since there is no mutation in this version. |

---

## 12. Out of scope

Not part of this design: any code path that changes a `.conf`
production, reload, snapshot, rollback, lint, routing simulation, server
MCP, log reading and multi-node orchestration. Each one enters for the version of
corresponding roadmap, with its own design cycle and plan.
