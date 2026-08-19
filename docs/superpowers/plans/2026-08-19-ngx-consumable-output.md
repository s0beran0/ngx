# ngx — Output a consumer can actually read

**Goal:** make the common case cheap. Reading one value out of `ngx` must not
require a second tool, and must not cost the caller the whole configuration.

**Spec:** `docs/superpowers/specs/2026-08-17-ngx-cli-design.md`, §5 (selector
language) and §6 (envelope).

## The measurement that justifies this plan

`ngx inspect` against a real production nginx — 132 files — emits **1.6 MB** of
JSON. For the audience this tool was built for, that is roughly 400,000 tokens
to answer a question like "which port does this server listen on".

The evidence is in how the tool was used while it was being built: almost every
invocation in that session went through `jq` or a Python one-liner to pull two
or three fields out. When the person who wrote the tool cannot use it without a
second tool, that is not the caller's problem.

Two costs, and they are different:

- **What is produced.** `inspect` builds the whole tree even when the caller
  wants one directive. Piping to `jq` does not fix this: the bytes were already
  produced, transferred and — over SSH — read from 132 remote files.
- **What has to be parsed.** Even for `status`, at 2.8 KB, getting `running`
  means a JSON parser. In a shell, that is `jq`; for an agent, it is tokens.

## Decisions

### DO1 — Not producing beats filtering

`ngx get <selector>` resolves the selector against the tree and returns **only
what matched**. It does not build the full envelope and then cut it down.

*Why:* filtering downstream only saves the caller's parsing. It does not save
the reading of 132 files over the network, the redaction of every value, or the
serialisation of a tree that gets thrown away. Over SSH, where the DR3 measured
one round trip per file, the difference is not cosmetic.

*Consequence:* the selector has to reach the reading layer, not only the
rendering one. Where an include's subtree cannot influence the match, it must
not be read.

### DO2 — One value comes out as one value

`--field <path>` on any command prints the raw scalar, no JSON, no quotes, no
envelope: `ngx --field data.nginx.version status` prints `1.20.1`.

*Why:* this is the case that showed up most while using the tool, and today it
costs a JSON parser. A path that does not exist is exit 2 with a diagnostic on
stderr — never an empty string on stdout, which a shell would happily assign to
a variable and carry on with.

*Limit:* `--field` addresses the **envelope**, with a dot path. It is not a
second selector language, and it does not compete with `get`: one navigates the
answer, the other chooses the question.

### DO3 — `--human` has to be human

Today `--human` prints indented JSON, which is documented in the README as
pending work rather than as a style. A human reading `status` wants three
lines, not a serialised object.

*Why here:* it is the same problem seen from the other side. The tool serves two
audiences and today serves one badly — and the human is exactly the one who
reaches for `jq` when the output is unreadable.

---

### Task O1: `--field`, the cheapest win

**Files:** Modify `internal/cli/root.go`, `internal/output/render.go`
**Test:** `internal/output/render_test.go`, `internal/cli/root_test.go`

- [x] **Step 1: Tests first**

An existing scalar prints raw with no trailing newline beyond one; a path into
an object prints compact JSON; a path that does not exist is **exit 2 with
nothing on stdout**; `--field` with `--json` is a usage error, because asking
for one field and the whole envelope at once has no coherent answer.

The case that matters: a missing path must not print an empty line. A test that
only checks the exit code would pass while a shell variable silently receives
an empty string.

- [x] **Step 2: Implement in the renderer**

`--field` is a rendering concern: the envelope is built normally and the
renderer selects. It applies to every command for free, which is the point.

- [x] **Step 3: Run and commit**

### Task O2: `inspect --summary`

**Files:** Modify `internal/cli/inspect.go`

- [ ] Envelope with `summary` and `config_hash`, without the tree. Against the
      measured production host this turns 1.6 MB into a few hundred bytes.
- [ ] It still parses everything — the counts require it — so this is about
      what is emitted, not about what is read. That distinction goes in the
      flag's help text, so nobody expects it to be faster over SSH.

### Task O3: `ngx get <selector>`

**Files:** Create `internal/selector/` (lexer, parser, eval); create
`internal/cli/get.go`

- [ ] **Step 1: Lexer and parser, with the four disambiguation rules**

§5 of the spec fixes R1 to R4 precisely because they only show up when writing
the lexer: `.` as a separator only outside brackets, `#` as ID at the start and
as index anywhere else, predicate about the node versus about a child, and
quantification over multiple arguments.

- [ ] **Step 2: Evaluation over the tree**

- [ ] **Step 3: Property test against `inspect`**

For any selector matching a node, the node returned by `get` has to be
byte-identical to the same node inside the full `inspect`. This is the oracle
that keeps the two commands from drifting into two different truths.

- [ ] **Step 4: Reading pruning (DO1)**

Only after the above is green. Reading fewer files is an optimisation, and an
optimisation on top of an unproven evaluator produces a wrong answer faster.

### Task O4: Human rendering that deserves the name

- [ ] `status`, `test` and `get` with a rendering meant for a terminal.
- [ ] `inspect` in human mode without `--summary` stays as it is: whoever asks
      for the full tree on a terminal knows what they are asking for.

## Verification

| Requirement | Task |
|---|---|
| Read one value without a second tool | O1 |
| Do not pay for the whole tree to ask a small question | O2, O3 |
| Not produce what will not be used | O3 step 4 |
| Serve the human as well as the program | O4 |

## Order

O1 first: it is the smallest, it applies to every existing command, and it
removes most of the `jq` from day-to-day use. O2 next, also small. O3 is the
big one and it is what the project promised on the first line of the README —
"reading by selector" — so it does not get pushed further.
