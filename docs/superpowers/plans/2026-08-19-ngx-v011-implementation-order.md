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

### A3 — Let the question choose the format

Measured on this codebase, same file three ways:

| | bytes |
|---|---|
| the nginx source text | **351** |
| the JSON tree of it | 2,635 |

Three questions, three answers, and no single format wins:

| Question | Format | Why |
|---|---|---|
| "how is site X configured?" | **nginx text** | 7.5x smaller than the JSON tree on the file measured, and the syntax every model already reads |
| "list every port / upstream / match" | **TSV** | flat and uniform; a realistic 269-match result is 47% fewer bytes and roughly 60% fewer tokens |
| "give me the structure to process" | **JSON** | lossless, argument boundaries preserved |

TOON was measured and rejected: on our real shape it came out **13% larger
than JSON**, because one field being a list (`args`) drops it out of its
tabular fast path. Flattening `args` to a string recovers 41% — still worse
than TSV's 47%, and at the cost of destroying the argument boundary. Recorded
as a negative decision so the next reader who sees "TOON saves 40%" does not
re-litigate it.

The same honesty applies to TSV: it flattens `args` too. A table is a **view**,
not a serialisation, and a lossy view is fine only when the loss is obvious and
a lossless option sits beside it.

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

## Holes found while reviewing this plan

Each of these was found by checking the plan against the code rather than
against itself. They are recorded with their consequence, because a plan that
hides its own weak points is worse than one that has none.

### H1 — `--format nginx` needs per-argument spans, which do not exist

**Blocking.** A node carries `span` (the whole directive) and `head_span`
(name **and** arguments together). Verified: for `ssl_certificate_key
/etc/ssl/secret.pem`, `head_span` covers the entire string. There is no span
per argument.

Emitting the source text therefore cannot redact: the value's byte range is
unknown. Emitting it raw would leak `ssl_certificate_key` and every password in
the configuration — the exact thing the redactor exists to prevent, bypassed by
a new output format.

*Consequence:* `--format nginx` costs one prerequisite, per-argument spans in
the aligner. That is not wasted work — v0.2 needs the same thing to replace one
argument without rewriting the directive — but it means the format is not the
cheap win it looked like. It moves behind that prerequisite in the order.

*Rejected alternative:* re-tokenising `head_span` at render time. It duplicates
tokeniser logic in a second place, and the two would drift. This codebase has
already paid for a divergence of exactly that kind.

### H2 — A filtered read makes `config_hash` a lie

`Hash` is computed over the tree it is given. Filtered with `--file`, the tree
is a subset, so the hash is a valid hash **of a subset** — and indistinguishable
from the hash of the whole thing.

That is harmless while reading and dangerous the moment v0.2 uses the hash for
optimistic locking: an agent could read filtered, get a hash, and apply a change
against a configuration the hash never covered.

*Demand:* a filtered result either omits `config_hash` — absence is
information, and the rule already exists in this project — or carries a scope
marker beside it. It must never look authoritative about a whole it never saw.

### H3 — `--server` cannot prune reads, only `--file` can

A1 says filters should reach the reading layer. That works for `--file`: read
the top file, expand the includes, read only what matches.

It cannot work for `--server`. Knowing which file declares a `server_name`
requires reading the file. The saving there is in what is **emitted**, not in
what is read.

*Demand:* the flags' help must not imply otherwise, and the plan must not
promise 132 round trips becoming one for `--server`. Overstating this would be
the kind of claim that gets discovered by a user timing it.

### H4 — TSV has no escaping rule yet

nginx arguments can contain quotes and, in principle, tabs. TSV with an
unescaped tab inside a field silently produces an extra column, and a consumer
reads a shifted row without any error.

*Demand:* pick and document one rule before shipping — escape, or refuse the
row with a diagnostic. Silent corruption is the worst of the three options.

### H5 — The 5 KB budget in the release gate is not well posed

"The answer must cost under 5 KB" is wrong as an absolute: a large site's
configuration is legitimately larger than that, and the gate would fail on
success.

*Demand:* state it relative — the filtered answer must be within a small factor
of the source file it describes, and orders of magnitude below the full dump.

### H6 — "The contract gets more expensive by waiting" is weaker than I wrote

v0.1.0 is already published, so the envelope shape is already public. Adding a
schema version is additive and safe, and reordering around urgency that has
partly already elapsed would be arguing from a fact that is no longer true.

*Correction:* the contract work goes first because it is cheap and unblocks
honest deprecation later, not because a deadline is passing.

## Coverage: what nginx surface this does NOT cover yet

The plan above assumes reading works for whatever the caller points it at. That
assumption was tested rather than trusted, by feeding the binary the
constructions most likely to break it.

**Covered, verified:** `stream` and `mail` blocks, with their own ID prefixes
(`st`, `m`) so a `server` in `stream` cannot collide with one in `http`; `map`,
`geo`, `split_clients` and `types`, whose bodies are free key/value pairs
rather than directives; regex and named locations; a value containing `;`
inside quotes; a directive from an unknown third-party module; `njs`
(`js_import`, `js_content`).

**Not covered, and it is a false rejection:**

### C1 — Embedded Lua breaks the parse

```
content_by_lua_block {
    if t.x > 0 then ngx.say("hi; bye") end
}
```

Refused with `NGX-0003`, complaining about an `if` "expression" that is Lua
code. nginx with `lua-nginx-module` accepts this file, so `ngx` refuses a valid
configuration — the worst class of bug this tool can have, and the one the
differential fuzz was built to catch.

Two things make it worth recording carefully rather than just fixing:

**It was introduced by a fix.** The `if` guard exists because `if ()` used to
crash the process. That guard now reads the `if` inside a Lua body as an nginx
directive. A correction created a new defect in a case nobody thought to test,
which is exactly what the "fix rounds are implementation" rule in
`writing-plans.md` warns about.

**The library already solves it.** crossplane v0.4.89 ships `lua.RegisterLexer()`
— a lexer extension that tokenises `*_by_lua_block` bodies as opaque content.
We never registered it. Rule one of this project, again: the answer was in the
dependency's source, not in our imagination.

*Consequence for this plan:* registering the extension is not the whole fix.
Our own tokeniser produces the byte spans that the aligner matches against
crossplane's token stream. If crossplane starts emitting one opaque token for a
Lua body while our tokeniser still emits its contents, the two desync and every
span after that point is wrong — silently. The Lua work therefore belongs with
the per-argument span work of H1, in the same phase, behind the same
differential test.

*Scope note:* OpenResty is common enough in production that this is not an edge
case dressed as one. It goes in 0.1.1.

## Phases

Two tracks on disjoint files: **R** (reading) and **P** (packaging). They meet
only at the documentation and the release.

### Phase 1 — Envelope contract  ‖  P1 install channel

R1: schema version, and a redacted value distinguishable from an absent one.

**Truncation moved out of this phase.** The plan claimed a truncated tree looks
complete; verified against the binary, it does not — a failed read yields
`ok:false` with `data:null`, no partial tree at all. Partiality only becomes
reachable when the filters of Phase 2 make it deliberate, so the marker belongs
there, next to what creates it. Recorded rather than silently dropped: the edge
case was real, my placement of it was not.

First because it is the only work here that gets **more expensive by waiting**:
every day, one more consumer depends on the current shape.

P1 in parallel: it turns every packaging task afterwards into configuration
rather than a design question.

### Phase 2 — Filters and `--query`  ‖  P2 `.deb`/`.rpm`/`.apk`

R2: `--file` and `--server` on `inspect`, matching the whole path. `inspect`
with no filter returns the summary.

R2b: **partiality marked inside `data`**, since R2 is what creates it. A
filtered tree is a subset by design, and an agent reading only `data` has to
trip over that fact where it is looking — together with the scope rule for
`config_hash` from H2.

R3: `--query` with embedded `gojq`. Discharges A2 on its own.

### Phase 3 — `get`, per-argument spans, and the formats  ‖  P3 tap, P4 AUR/Scoop/winget

R4: `get` with flat flags — `--directive`, `--in`, `--value`. No grammar to be
subtly wrong about. Property test against `inspect`: a node from `get` must be
byte-identical to the same node in the full tree, which is what keeps the two
commands from drifting into two truths.

R5: **per-argument spans in the aligner** (H1). Prerequisite for anything that
renders source text, and the same thing v0.2 needs to replace one argument
without rewriting the directive. It gets its own differential test against the
tokeniser, like every span work in this codebase.

R6: `--format nginx`, only after R5. Redaction is applied by byte substitution
over the argument spans — which is a rehearsal of the v0.2 edit path, performed
where getting it wrong corrupts nobody's file.

R7: `--format table` for flat results, with the escaping rule decided in H4.

R8: **register crossplane's Lua lexer** (C1). It ships `lua.RegisterLexer()`
and we never registered it, so a `content_by_lua_block` whose body contains an
`if` is refused today — a valid configuration rejected, which is the worst
class of defect this tool has.

It sits here rather than earlier for one reason: crossplane will start emitting
a single opaque token for a Lua body while our tokeniser still emits its
contents. That desyncs the aligner and silently corrupts every span after the
block, so it needs the differential test of R5 already in place. Registering
the lexer without that test would trade a loud refusal for a quiet wrong
answer.

**Read pruning comes last within this phase**, after the property test is
green, and only for `--file` (H3). An optimisation on top of an unproven
evaluator produces a wrong answer faster.

### Phase 4 — `--help` that teaches, human rendering, docs, release

Documentation last: written earlier it documents a command as imagined, which
this project has already paid for twice.

## The breaks 0.1.1 carries, and why `schema_version` stays at 1

Two changes in this release break a consumer written against v0.1.0:

1. **`inspect` no longer returns the tree by default.** `data.config` is absent
   unless `--full-tree` is given. On the measured production host this is the
   difference between 1.6 MB and a few hundred bytes, which is the point of the
   release — but code that read `data.config` gets nothing.
2. **A redacted directive keeps its other arguments.** `["***"]` became
   `["Authorization", "***"]` with `redacted_args: [1]`. Better, and breaking
   for anyone reading `args[0]`.

`schema_version` stays at **1** rather than moving to 2, and the reasoning
matters more than the number: v0.1.0 declares no schema version at all, so
nothing was ever published as version 1. The field is introduced in 0.1.1 and
**describes 0.1.1's shape**. Anything without it is pre-contract. Bumping to 2
would imply a version 1 existed in the wild, which would be a lie told by a
version number.

*The obligation this creates:* the release notes must enumerate both breaks in
words. A consumer that never reads `schema_version` — which is most of them,
today — has no other way to find out, and "it was in the schema version" is not
a defence for silence.

## What the Lua work left open

### The oracle now exists, and it found six divergences

A second bench image with OpenResty 1.27.1.2 closed the hole. It paid for
itself immediately: **six** measured divergences, all recorded as tests rather
than prose, because a divergence written in markdown ages in silence.

The answer that was missing: `local s = 'a\'b'` is **accepted by OpenResty and
refused by ngx**. The backslash escapes nothing in crossplane's lexer, so the
second quote closes the string and the block's `}` falls "inside quotes". Same
for `"a\"b"`. The upstream issue is now justified by evidence instead of by
assumption.

Four of the six are false rejections in narrow escaping forms — bad, and
visible. One is a false rejection by OpenResty (an empty Lua body, which it
refuses as "no runnable Lua code", a semantic check ngx does not make).

**The sixth is the serious one, and it changes what v0.2 may assume.**

`content_by_lua_block { -- }` — a Lua comment containing a brace. Crossplane's
lexer closes the block early, so whatever follows lands in the wrong scope.
ngx **accepts** the file and builds a tree; OpenResty **refuses** it. So ngx
describes a structure the running server never had, and nothing in the output
says so.

*For v0.1.1 this is a wrong reading, and `ngx test` still gives a correct
answer about validity because it delegates to nginx itself.*

*It was a blocking prerequisite for v0.2, and it is now CLOSED.* Not upstream,
and not by a fork: crossplane exposes `Lexer` as a public interface
(`lex.go:48-53`) and `LexWithLexer` as the way to register one, so ngx
registers its own (`internal/config/luascan.go`, `lualexer.go`). The
delimitation follows Lua's rules — braces counted in code, and not inside a
short string with escapes, a long bracket, or either kind of comment — and it
lives in one state machine because two readers need it and must not disagree by
a byte: the tree crossplane builds and the byte spans our tokenizer produces.

Until then the tokenizer *deliberately replicated* the dependency's wrong rule,
because being right alone would have desynchronised the two streams and pointed
spans at the wrong text. That constraint is what made "wait for upstream" look
inevitable, and it dissolved the moment the lexer became ours.

Verified against OpenResty 1.27.1.2: the four rows that were divergences are
agreements, and the case that made ngx accept a file the server refuses now
makes ngx refuse it too. One divergence stays open on purpose — an empty body,
which lua-nginx-module rejects as "no runnable Lua code", a judgement about what
the code DOES rather than where it ends.

*Test design worth copying:* the case asserts that OpenResty still refuses AND
that ngx still accepts. If either side changes — an upstream fix, a lexer
update — the test fails and the note gets revisited instead of quietly
outliving its truth.

### The second v0.2 blocker: an ID does not name one node

Found by the release gate's own agent test, not by a unit test — asking the
bench "which ports are listened on" returned 112 matches carrying **one**
distinct ID. Every one of them was `s0.d0`.

The cause is not the privileged read path, it is `include`. IDs are assigned
per file (`parse.go:121`), so they count siblings from the root of the file
they live in. On the layout every distribution ships — one site per file under
`conf.d/*.conf` — the first `server` of every file is `s0` and its first
directive is `s0.d0`. The `h.` prefix disappears too, because the included
fragment has no enclosing `http` of its own. `Combine` assigns IDs over the
assembled tree and does separate them, but it is not the default of any
command: `get` never combines and `inspect` only does with `--combine`.

*For v0.1.1 nothing reads back an ID*, so no command resolves one to the wrong
node. IDs are output-only, and every output that carries one carries its `file`
next to it, which is what actually scopes it.

*It was a blocking prerequisite for v0.2, and it is now CLOSED.* The reference
is `Node.Ref`, `"<file>#<id>"`. The query that returned 112 matches with one
distinct ID returns 112 matches with 112 distinct refs.

The three candidates, and why the middle one won:

- *Assign IDs over the whole configuration.* Makes them unique, and destroys
  the stability D3 promises: dropping a new file into `conf.d/*.conf` would
  renumber servers in files nobody touched.
- *Make the reference `(file, id)`.* **Chosen.** The file is the natural scope
  and scoping by it costs nothing — adding, removing or renaming a file cannot
  renumber another one — and it is what the output already showed side by side.
- *Make `--combine` the addressing tree.* Unique and stable against sibling
  files, but the IDs belong to a view the caller has to ask for, so a reference
  would mean nothing without the flag that produced it.

So **Ref is identity and ID is position in the current view.** That split is why
`AssignRefs` is separate from `AssignIDs`: `Combine` renumbers ID over the
assembled tree, which is what makes `h.s0` read sensibly there, and leaves Ref
alone — a caller can read through one view and act through the reference it saw.
`FindByRef` cannot be ambiguous; `FindByID` stays, still refusing the ambiguous
case, because an ID is what a human reads off a table.

It answers D3's question the same way D3 does: a reference is anchored in SPACE
by the file and in TIME by `config_hash`. Neither substitutes for the other.

*Contract note:* the `get` table now carries one `ref` column where it carried
`id` and `file`. JSON is additive — `id`, `file` and `ref` are all present — so
nothing reading the envelope breaks.

### Where the oracle itself could lie

`openresty -t` does **not compile** the Lua body: `{ if end }` passes. It only
lexes far enough to find the block's end — which happens to be exactly the
question ngx asks, so the oracle is well matched. `smoke-lua.sh` carries a
dedicated property proving the oracle can still say no, because an oracle that
accepts everything is a rubber stamp that reads like a guarantee.

### The old note, kept for the record

The bench runs stock nginx 1.20.1, which has no `lua-nginx-module`: it refuses
`content_by_lua_block` outright as an unknown directive. So the fixture that
makes "compatible with nginx" mean something **cannot validate the Lua path** —
verified, not assumed.

Everything claimed about Lua acceptance therefore rests on crossplane's lexer
and on reasoning about Lua syntax, which is exactly the kind of ground this
project has been burned on before. Two ways out, and the choice is a cost
decision rather than a technical one:

- a second bench image with OpenResty, giving a real oracle for the Lua path;
- or documenting plainly that Lua support is validated against crossplane only.

Doing neither is the bad option: it leaves a claim with nothing behind it.

### An inherited limitation: `\'` inside a Lua string

crossplane's Lua lexer does not understand an escaped single quote, so
`local s = 'a\'b'` is refused. Real nginx with the module almost certainly
accepts it — but "almost certainly" is precisely what could not be checked,
for the reason above.

There is no fix available on our side: the dependency's lexer fails on its own.
The honest path is upstream — a report, and ideally a patch, to
nginx-go-crossplane. Working around it locally would mean forking the lexer,
which this project rejected once already when it chose to build spans on top of
crossplane rather than vendor a fork.

*Net effect is still positive:* before this work, **every** Lua block with an
`if` was refused. Now one escaping form is.

### A defect fixed outside the scope, worth keeping visible

`readVar` had copied the CRLF rule from `readWord`, so `"${\r\nx"` produced two
tokens against crossplane's one — a real aligner desync that predates the Lua
work and would silently shift every span after it. Found while building the
differential test, fixed, and seeded into the fuzz corpus.

It is worth noticing *how* it surfaced: the differential test had been
comparing against an oracle that lexed a different language, because it never
registered the Lua extension. Making the oracle honest is what exposed a bug
that had nothing to do with Lua.

## Release gate

The 0.1.0 gate, plus:

- `.deb` and `.rpm` installed **in containers**, checking `install_channel` and
  that `ngx update` refuses
- against the production nginx, read-only: a filtered `inspect` returning a
  subtree byte-identical to the corresponding slice of the full one
- `internal/config/testdata/syntax_surface.conf` still accepted by real nginx in
  the container, and its values still parsed exactly. It is the fixture that
  says what "compatible with nginx" means here, and it only means anything
  while nginx keeps agreeing with it.
- **the agent test**: answer three real questions starting from `ngx --help`
  alone — which ports are listened on, what a given site's configuration looks
  like, whether the configuration is valid. If any needs the spec, Phase 4 is
  not done.
- **the token budget, stated relatively** (H5): the answer to "show me this
  site's config" must be within a small factor of the source file it describes,
  and orders of magnitude below the full dump. An absolute ceiling would fail on
  a legitimately large site.
- **no filtered result carries an authoritative `config_hash`** (H2): either it
  is omitted or it is scoped. A hash that describes a subset while looking like
  it describes the whole is how a v0.2 client applies a change against something
  it never read.

## What this sets up for v0.2

Everything here is a reading tool, and mutation needs exactly these to be safe:
the filter that finds one file is what an edit will target; the byte spans
already carried by every node are what makes the edit a substitution rather
than a re-render; and the schema version is what lets a v0.2 client refuse to
apply a plan built against a shape it does not know.
