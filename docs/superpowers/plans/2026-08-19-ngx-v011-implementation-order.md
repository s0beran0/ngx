# ngx v0.1.1 — From pilot to usable

**Goal:** turn a tool that works into a tool that is worth using. v0.1.1 is
still read-only; it is about making reading cheap, discoverable and complete
enough that v0.2 can be about writing.

**Consolidates and supersedes:** `2026-08-19-ngx-consumable-output.md` and
`2026-08-19-ngx-distribution-channels.md`, plus the conclusions of
`../specs/2026-08-19-ngx-agent-ergonomics.md`.

## Why 0.1.1 and not 0.2.0

`get` and the filters come from the v1.0 spec that started this project and
were in the v0.1 design from day one. Shipping them **completes v0.1** rather
than extending it. And **v0.2 is reserved for mutation**: someone reading
"v0.2" in this project should understand "it writes now".

## Measured baseline

Everything below is measured on this codebase or against the production nginx
it was validated on, not estimated.

| | today |
|---|---|
| `inspect` on a 132-file configuration | **1.6 MB**, order of 400k tokens |
| `status` | 2.8 KB, needs a JSON parser to answer "is it running" |
| SSH round trips for one `inspect` | **132**, one per file |
| A flat result of 269 matches, JSON vs tabular | **47% fewer bytes, ~60% fewer tokens** in tabular |
| `jq` present on the validated production host | **no**. Neither was `minisign` |
| Examples in `--help` | **zero** |

## Four axes, none of which replaces another

This plan was re-centred twice on whatever had just been discussed. The costs
are distinct and so are the answers; shipping one does not discharge another.

### A1 — Do not read or produce what was not asked for

`inspect` reads 132 files over SSH and emits 1.6 MB to answer a question about
one file. Filtering downstream undoes none of it.

*Answer:* filters that reach the reading layer, in the domain's vocabulary
rather than ours — `--file sesc-portal`, `--server portal.example.com`. This is
what replaces `grep`, and it answers only this axis.

### A2 — Do not require a second program to read the answer

Even 2.8 KB needs a parser to answer one question, and `jq` was not installed
on the host this project was validated against.

*Answer:* `--field` for one value, already shipped, and `--query` with an
embedded `gojq` for anything more. Verified viable: pure Go, MIT, one indirect
dependency, cross-builds on all six platforms without cgo.

`jq` syntax costs an LLM nothing to learn — counting it as onboarding was an
error recorded here so it is not repeated. What `jq` does require is knowing
the envelope shape, which belongs to A4.

### A3 — Let the shape of the data choose the format

Research on token-optimised formats converges on this: for flat, uniform data,
tabular formats cost around half of JSON; for data nested beyond two levels,
JSON is the only practical option and YAML saves about a quarter.

Our output has both shapes. A configuration tree is deeply nested. A result
list — matches, files, diagnostics — is flat and uniform, and **measured on our
own data it is 47% smaller and roughly 60% cheaper in tokens as a table**.

*Answer:* `--format table` for flat results, JSON everywhere else, and never a
table for the tree. The rule is the data's shape, not a preference.

### A4 — Usable without reading a specification

An agent should answer its first question having read only `ngx --help`.

*Answer:* examples in the help text; a schema version in the envelope; a
redacted value distinguishable from an absent one; truncation visible inside
`data`.

## Edge cases, and what each one demands

Failure modes of somebody *using* the tool. Each was reached by asking "what
does the caller do next?" and finding no good answer.

### Finding a file — the case that motivated the filters

| Case | Demand |
|---|---|
| `--file sesc` matches several | List the candidates and exit non-zero. Never pick one. A tool that guesses teaches nobody to be precise. |
| Matches nothing | Say so, and say what **was** available. An empty result and a wrong name look identical otherwise. |
| Matches a file that exists but cannot be read | Already handled: permission is its own class and the message names `--sudo`. |
| Full path vs fragment | Both work. A fragment is a substring of the path; a path that starts with `/` is exact. No globbing rules to learn. |
| The same basename in two directories | Ambiguity, so the rule above applies — and it is why matching is against the whole path, not the basename. |
| `--file` and `--server` together | AND, and it has to be documented, because the other reading is equally plausible. |

### Reading and output

| Case | Demand |
|---|---|
| The dump blows the caller's context | The full tree is not the default. Asking for everything is explicit and the flag name says what it costs. |
| A redacted value looks absent | An agent that cannot tell censorship from absence retries in a loop or reports an empty key. The data has to distinguish them. |
| A truncated tree looks complete | `ok` is already false, but an agent reading only `data` must trip over the gap where it is looking. |
| The contract moves between versions | A schema version, separate from `ngx_version`, which changes on releases that change nothing about the shape. |
| A table is asked for on nested data | Refuse with a reason, do not flatten. A silently flattened tree is a wrong answer that looks like an answer. |

### Distribution

| Case | Demand |
|---|---|
| `apt`/`brew` owns the binary and `ngx update` swaps it | The channel is a build-time fact; a packaged `ngx` refuses and names the right command. |
| A build from source has no channel | Default `direct`, so `go build` gives a working updater. |
| A packager mistypes the channel flag | An unknown value refuses too. A typo must not re-enable self-update. |

## Phases

Two tracks on disjoint files: **R** (reading) and **P** (packaging). They meet
only at the documentation and the release.

### Phase 1 — Envelope contract  ‖  P1 install channel

R1: schema version, redaction distinguishable from absence, truncation visible
in `data`.

First because it is the only work here that gets **more expensive by waiting**:
every day, one more consumer depends on the current shape.

P1 in parallel: it turns every packaging task afterwards into configuration
rather than a design question.

### Phase 2 — Filters and `--query`  ‖  P2 `.deb`/`.rpm`/`.apk`

R2: `--file` and `--server` on `inspect`, matching the whole path. `inspect`
with no filter returns the summary.

R3: `--query` with embedded `gojq`. Discharges A2 on its own.

### Phase 3 — `get` and `--format table`  ‖  P3 tap, P4 AUR/Scoop/winget

R4: `get` with flat flags — `--directive`, `--in`, `--value`. No grammar to be
subtly wrong about. Property test against `inspect`: a node from `get` must be
byte-identical to the same node in the full tree, which is what keeps the two
commands from drifting into two truths.

R5: `--format table` for flat results.

**Read pruning comes last within this phase**, after the property test is
green. An optimisation on top of an unproven evaluator produces a wrong answer
faster.

### Phase 4 — `--help` that teaches, human rendering, docs, release

Documentation last: written earlier it documents a command as imagined, which
this project has already paid for twice.

## Release gate

The 0.1.0 gate, plus:

- `.deb` and `.rpm` installed **in containers**, checking `install_channel` and
  that `ngx update` refuses
- against the production nginx, read-only: a filtered `inspect` returning a
  subtree byte-identical to the corresponding slice of the full one
- **the agent test**: answer three real questions starting from `ngx --help`
  alone — which ports are listened on, what a given site's configuration looks
  like, whether the configuration is valid. If any needs the spec, Phase 4 is
  not done.
- **the token budget**: the answer to "show me this site's config" must cost
  under 5 KB. Today the only way to get it is the 1.6 MB dump.

## What this sets up for v0.2

Everything here is a reading tool, and mutation needs exactly these to be safe:
the filter that finds one file is what an edit will target; the byte spans
already carried by every node are what makes the edit a substitution rather
than a re-render; and the schema version is what lets a v0.2 client refuse to
apply a plan built against a shape it does not know.
