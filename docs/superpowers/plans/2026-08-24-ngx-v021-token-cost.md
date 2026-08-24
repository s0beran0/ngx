# ngx v0.2.1 — What an answer costs

**Goal:** measure what every command charges the caller, and cut the part of it
nobody reads. No new capability: the same questions, answered for fewer tokens.

**Spec:** `docs/superpowers/specs/2026-08-17-ngx-cli-design.md`, §6 (envelope).
**Predecessor:** `2026-08-19-ngx-consumable-output.md`, which cut what is
*produced*. This one cuts what is produced *per node*.

## The measurement

Against a 61-file configuration — one `server` per file, which is what a
distribution's `conf.d` looks like — counted with tiktoken `o200k_base`:

| question | command | tokens |
|---|---|---|
| which ports are listened on | `get --directive listen` | 5537 |
| the same, as a table | `get --directive listen --format table` | 1660 |
| what is in one file | `inspect --file site-7.conf` | 1023 |
| the whole tree | `inspect --full-tree` | 53475 |

`o200k_base` is OpenAI's tokenizer and not the one every consumer uses. It is
used here because it is available offline and because the numbers that matter
are *ratios between two forms of the same answer*, which barely move between
tokenizers. Where an absolute number appears below, read it as an order of
magnitude, not a contract.

Where the 5537 goes, field by field:

```
ref            19.3%   it is "<file>#<id>", so it repeats both
file           15.8%   605 occurrences for 61 files
span ends      31.6%   only an edit needs them
id              7.1%
line + column  10.2%
directive       5.8%   <- the answer
```

**The field that answers the question is 5.8% of what is charged for it.** Byte
ranges alone are five times the directive.

## Decisions

### DO1 — A level of detail, not a smaller default

`--detail answer` emits directive, args, file, line, ref and id. `--detail full`
— the default — emits everything, byte spans included.

*Why a flag and not the default:* the envelope is a contract and
`schema_version` exists to say when it changed. Dropping `span` from the default
output is exactly the break that field is for, and v0.2.1 is a patch. The case
for flipping it is recorded below, with numbers, for a version that can afford
the bump.

*Why `answer` still carries `ref`:* a reader that cannot name what it found has
to ask again to act on it, and the second question costs more than the field.
`ref` is what `ngx set` takes.

### DO2 — A type, not the full node with fields zeroed

`leanNode` is a separate struct. The first attempt set the unwanted fields to
their zero value and left the shape alone; it saved **15%** where the arithmetic
promised 42%.

*Why it failed:* `Span`, `HeadSpan` and `Column` carry no `omitempty`, and that
is deliberate — on a full node a span of `[0,0)` is a real value, and absence
has to stay distinguishable from zero. So the keys went out with zeroes inside
them and cost nearly what they cost full.

Adding `omitempty` to those tags would have fixed the number and broken the
meaning. A separate type fixes the number, leaves the full form alone, and is
the honest way to say what a level contains: a struct somebody can read rather
than a subtraction somebody has to infer.

### DO3 — The table extracts the shared path once

In `--format table`, when every row's ref sits under one directory, that
directory is printed once as a comment and stripped from the rows.

*Why the comment and not a column:* the prefix is not data about a row, it is
data about the answer. A caller that splits on tabs is unaffected; one that
reads the whole output gets told, in one line, what the paths are relative to.

*What was rejected, with the measurement:* grouping the rows under a per-file
header. It measured **30% worse**. A header costs more than the repetition it
saves when there is roughly one match per file — which is the shape of every
"find this directive across the sites" question. The obvious idea was the wrong
one, and this is the only record of that.

### DO4 — The cost is a checked property, not a claim

`internal/cli/tokencost_test.go` asserts a byte budget per question and a ratio
between `full` and `answer`. Nothing else in the suite would notice a command
that quietly triples its output: the formats were tested for correctness and
never for size.

*Why bytes:* a Go test has no tokenizer, and vendoring one to approximate a
model nobody here runs would be precision theatre. Bytes are exactly measurable
and move with tokens for output of this shape.

*One correction the test has to make:* a path under `t.TempDir()` is around 85
characters against 18 for `/etc/nginx/conf.d`. Both `file` and `ref` survive
into the lean form, so the harness's long path inflates the *cheap* side and
understates the saving — 0.72 measured with the temporary path against 0.58 with
a realistic one, for identical code. The test normalises the prefix, because
what it normalises is an artefact of where the test writes and nothing about the
product.

*And it has to be able to fail.* Both properties were verified against a
deliberately broken build: with `--detail answer` made a no-op the ratio
assertion fails, and with the prefix extraction disabled both the table
assertion and the table's own budget fail. This project has shipped checks that
could not fail before; the verification is the point.

## Result

| question | full | answer | cut |
|---|---|---|---|
| `get --directive listen` | 5537 | 3198 | **42%** |
| `inspect --full-tree` | 53475 | 31857 | **40%** |
| `inspect --file` | 1023 | 665 | **35%** |
| `get --format table` | 1660 | 1373 | **17%** (DO3, no flag) |

The table is already the cheapest form of the commonest answer, and DO3 makes it
cheaper without a flag: a caller that never reads this document pays less.

## Left open

**Flipping the default.** `--detail answer` as the default would take the
commonest query from 5537 tokens to 3198 for every caller, including the ones
who will never learn the flag exists. It requires `schema_version: 2`, a
migration note, and a decision about whether `get` and `inspect` should differ —
`get` answers a question, `inspect` is often the input to an edit, and only the
edit needs spans. Deferred to the version that bumps the schema.

**Remote editing** was considered for this version and moved to v0.2.2. It is a
capability, not a cost, and mixing the two would have made neither measurable.
