# ngx — Using it without onboarding

**Question this document answers:** what has to be true for an AI agent to use
`ngx` correctly on its first try, without reading a specification, without
installing anything, and without spending its context on output it did not ask
for.

## The mistake this document exists to avoid

`gojq` embedded would remove the dependency on `jq`. It would not remove the
**onboarding**, and onboarding is the larger cost. An agent that has to write
`.diagnostics[].code` needs to know two things first: the jq language, and the
shape of our envelope. Add the selector language of §5 and that is three
specifications to read before asking one question.

The cost is not hypothetical. Measured while building this project: `ngx
inspect` against a 132-file configuration emits **1.6 MB** — on the order of
400,000 tokens. An agent that runs it once to find a `listen` directive has
spent more context on the answer than the whole task was worth.

So the target is not "a better query language". It is **not needing one**.

## What the tool already gets right

Verified against the current binary, not assumed:

- **JSON is compact**, single line, no indentation. Nothing to strip.
- **Deterministic output**: two runs over the same file are byte-identical.
  An agent can diff two runs and trust the difference.
- **Exit codes carry meaning**, so success does not require parsing.
- **Errors say what to do.** This is the tool's strongest agent-facing trait
  and it was not designed for agents: the `--sudo` hint names the flag that
  fixes it, the unknown-host refusal hands over the exact `known_hosts` line.
  Research on agent-facing CLIs converges on the same rule — an error is a road
  sign, not a report.

## Edge cases, and what each one demands

These are failure modes of an agent using the tool, not of the tool itself.
Each was reached by asking "what does the agent do next?" and finding no good
answer.

### E1 — The dump blows the context

`inspect` on a real configuration returns 1.6 MB. The agent has no way to know
that before running it, and by the time it knows, it has paid.

*Demands:* the full tree stops being the default. `inspect` without a filter
returns the summary and says how to get more. Asking for everything is
explicit, and the flag name says what it costs.

### E2 — The agent does not know the field names

To write any query, the agent has to know the envelope has `data.nginx.version`
and not `data.version`. Today that knowledge lives in a spec it will not read.

*Demands:* `--help` carries copy-pasteable examples showing intent, because
that is the documentation surface an agent actually reads. And the tool can
describe its own output shape without the agent guessing.

### E3 — A query language is a specification

The selector language of §5 has four disambiguation rules that exist precisely
because the grammar is ambiguous. Any agent that gets one wrong gets a silently
different answer.

*Demands:* the common questions are answered by **commands with flat flags**,
not by expressions. `--directive listen` beats `http.server.listen`, has no
grammar, and cannot be subtly wrong. A query language may still exist for the
rare case, but nothing common may require it.

### E4 — Redaction hides exactly what was asked for

An agent asking for `ssl_certificate_key` gets `***`, correctly. If it does not
understand that the value was withheld rather than absent, it retries, or it
reports the key as empty.

*Demands:* a redacted value has to be distinguishable from a missing one in the
data itself, not only in prose. Absence and censorship are different facts.

### E5 — The agent cannot tell a wrong answer from a partial one

Read a configuration where one file is unreadable and the tree is missing a
`server`. Today the diagnostic says so — but an agent that reads only `data`
sees a complete-looking tree.

*Demands:* `ok` must already be false in that case (it is), and any
truncation must be visible **inside** `data`, where an agent that skipped the
diagnostics still trips over it.

### E6 — The contract moves under the agent

An agent that learned `data.nginx.version` in 0.1.1 breaks silently in 0.2 if
the field moves. It has no way to notice except by getting `null`.

*Demands:* the envelope declares its own schema version, and that version is
part of the compatibility promise. `ngx_version` is not enough: it changes on
every release, including those that change nothing about the shape.

### E7 — The agent pays for what it did not ask for

`inspect` reads 132 files over SSH to answer a question about one. That cost is
invisible in the output and real in wall-clock time.

*Demands:* filtering has to reach the reading layer, not only the rendering
one. This is already recorded as DO1 in the output plan.

## The rule that follows

**The common case must be answerable by a command whose name is the question,
with flat flags and a small answer.** Whatever is left over may need a query
language, and that one should be `jq` syntax through an embedded engine rather
than something of our own — a language the agent already knows beats a language
we designed, however elegant.

An agent's first successful use of `ngx` should require reading nothing but
`ngx --help`.
