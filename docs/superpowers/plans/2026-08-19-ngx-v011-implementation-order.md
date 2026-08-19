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

## What the ergonomics research changed

The first version of this plan had `ngx get <selector>` as its centrepiece,
carrying the §5 expression language. That was the wrong centre.

Embedding a query engine removes the dependency on `jq` and leaves the
**onboarding** untouched, and onboarding is the larger cost: an agent needs the
language, plus the envelope shape, plus the selector grammar — three
specifications before one question. So the shape of `get` changes: **flat
flags, no grammar**. `--directive listen` cannot be subtly wrong the way
`http.server.listen` can, because there is nothing to get wrong.

The expression language is not cancelled; it is demoted. Nothing common may
require it, and when it does arrive it should be `jq` syntax through an
embedded engine rather than a grammar of ours — a language the agent already
knows beats one we designed.

Two other conclusions promoted themselves to the front of the queue, and the
reason is timing rather than importance: they change the **envelope contract**,
and every day they wait, one more consumer depends on the current shape.

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

### Phase 2 — The default stops being a dump (E1) ‖ install channel (C1)

`inspect` without a filter returns the summary and says how to ask for more.
The full tree needs an explicit flag whose name states its cost. 1.6 MB is not
a default; it is a decision the caller has to make on purpose.

C1 runs alongside — different files, and it is what turns every packaging task
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

### Phase 4 — `--help` that teaches (E2), and the escape hatch

Help text is the documentation surface an agent actually reads, so it carries
copy-pasteable examples showing intent rather than syntax. A first successful
use of `ngx` should require reading nothing else.

Only here, if it is still needed after Phases 2 and 3: `--query` with an
embedded `gojq`. Verified as viable — pure Go, MIT, one indirect dependency,
cross-builds on the six platforms without cgo. It is an escape hatch for the
rare question, not the way the tool is meant to be used.

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
