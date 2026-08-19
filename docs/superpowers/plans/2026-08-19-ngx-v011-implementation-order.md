# ngx v0.1.1 — Implementation order

**Goal:** deliver everything that still fits inside read-only, in an order
where each step is usable on its own and nothing is built on an unproven base.

**Consolidates:** `2026-08-19-ngx-consumable-output.md`,
`2026-08-19-ngx-distribution-channels.md`, and the conclusions of
`../specs/2026-08-19-ngx-agent-ergonomics.md`, which rewrote a good part of the
first one.

## Why 0.1.1 and not 0.2.0

`get` and `--field` are not new features. They come from the v1.0 spec that
started this project and were in the v0.1 design from day one — §5 defines the
selector language in full, down to the four disambiguation rules. Shipping them
**completes v0.1** rather than extending it, and a patch number says exactly
that.

The roadmap agrees from the other side: **v0.2 is reserved for mutation**.
Someone reading "v0.2" in this project should understand "it writes now".

## Three independent axes, none of which replaces another

This plan was re-centred twice, each time on whatever had just been discussed,
and each time that was wrong. The costs below are distinct, they have distinct
answers, and shipping one does not discharge the others.

### Axis 1 — Production cost: do not read or emit what was not asked for

`inspect` reads 132 files over SSH and emits 1.6 MB to answer a question about
one file. Filtering downstream does not undo any of that.

*Answer:* filters that reach the reading layer. The vocabulary is the domain's,
not the tool's: `--file sesc-portal` and `--server portal.example.com` are
things the caller already knows the name of. Substring matching, no globbing
rules to learn, and an unambiguous error listing the candidates when a pattern
matches several.

This is what replaces `grep`, and it is **additional** — it does not answer
either of the axes below.

### Axis 2 — Parse cost and external dependency

Even a 2.8 KB `status` needs a JSON parser to answer "is it running", and today
that parser is `jq`, which was **not installed** on the production host that
this project was validated against — exactly as `minisign` was not.

*Answer:* `--field` for a single value, already shipped, and `--query` with an
embedded `gojq` for anything more. Verified viable: pure Go, MIT, one indirect
dependency, cross-builds on all six platforms without cgo.

An LLM already knows `jq` — that syntax costs it nothing to learn, and I was
wrong to count it as onboarding. What `jq` does require is knowing the shape of
our envelope, which is Axis 3, not this one.

### Axis 3 — Onboarding: usable without reading a specification

An agent should answer its first question having read only `ngx --help`.

*Answer:* help text carrying copy-pasteable examples of intent; a schema
version in the envelope so a consumer can notice the contract moved instead of
discovering it through a `null`; a redacted value distinguishable from an
absent one, because an agent that cannot tell censorship from absence either
retries in a loop or reports an empty key; and truncation visible inside
`data`, where an agent that skipped the diagnostics still trips over it.

The `get` command keeps flat flags for the same reason — no grammar to be
subtly wrong about — but flat flags are an Axis 1 and 3 answer, not a
replacement for `jq`.

## Order, and why this one

### Phase 1 — Envelope contract (E4, E5, E6)

First, because it is the only work here that gets **more expensive by waiting**
rather than merely later.

- **Schema version in the envelope.** `ngx_version` is not enough: it changes
  on every release, including those that change nothing about the shape. An
  agent that learned a field needs a way to notice the contract moved that is
  not "I started getting null".
- **A redacted value is distinguishable from an absent one, in the data.** An
  agent asking for `ssl_certificate_key` and getting `***` today cannot tell
  censorship from absence, so it retries or reports an empty key. Absence and
  censorship are different facts and the data has to say which.
- **Truncation visible inside `data`.** When a file could not be read, `ok` is
  already false and the diagnostic says so — but an agent reading only `data`
  sees a complete-looking tree. It must trip over the gap where it is looking.

### Phase 2 — Filters and `--query` (axes 1 and 2) ‖ install channel (C1)

Two deliverables, both small, both independent of each other:

**`--file` and `--server` on `inspect`.** Substring match against the domain's
own vocabulary. `inspect` without any filter returns the summary and says how
to ask for more: 1.6 MB is not a default, it is a decision the caller makes on
purpose, and the flag that asks for everything says so in its name.

**`--query` with an embedded `gojq`.** Delivered here and not later, because it
discharges the "no external dependency" goal on its own and costs an LLM
nothing to learn. It filters what was produced; it does not reduce what is
read, which is why it does not replace the filters above.

C1 runs alongside — different files, and it turns every packaging task
afterwards into configuration instead of a design question.

### Phase 3 — `ngx get` with flat flags (E3) ‖ packages (C2, C3, C4)

`--directive`, `--in`, `--value`, `--file`. No expression, no grammar, no
precedence to learn. The property test against `inspect` stays exactly as
planned: a node returned by `get` has to be byte-identical to the same node in
the full tree, which is the oracle that keeps the two from drifting into two
truths.

**Read pruning comes last within this phase**, after that oracle is green.
Reading fewer files is an optimisation, and an optimisation on top of an
unproven evaluator produces a wrong answer faster.

### Phase 4 — `--help` that teaches (E2)

Help text is the documentation surface an agent actually reads, so it carries
copy-pasteable examples showing intent rather than syntax. A first successful
use of `ngx` should require reading nothing else — including the examples for
the filters and for `--query` shipped in Phase 2.

### Phase 5 — Human rendering, documentation, release

Documentation last, on purpose: written earlier it documents a command as
imagined. This project already paid for that twice.

## What was dropped, and why it is worth saying

`--field` shipped in Phase 1 of the previous ordering and stays: a dot path
with no operators has no grammar to get wrong. What did not survive is the plan
to grow it — the `[]` projection construct — because that is where it would
have started becoming a language. Projection belongs to `--query`, or to a flag
on `get`, and not to a syntax of ours growing one operator at a time.

## Verification before the tag

The same gate as 0.1.0, plus what this release adds:

- `make verify` and the integration suite with the bench up
- `.deb` and `.rpm` installed in containers, checking `install_channel` and
  that `ngx update` refuses
- against the production nginx, read-only: `get` returning a subtree identical
  to the corresponding slice of `inspect`
- **the agent test:** answer three real questions using only `ngx --help` as a
  starting point — which ports are listened on, which upstream a location
  proxies to, whether the configuration is valid. If any of them needs the spec,
  Phase 4 is not done.
- the published binary re-validated after the release, as in 0.1.0
