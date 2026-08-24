# What a CLI costs an agent — the literature, and where ngx stands against it

**Question:** v0.2.1 cut the output of `get` and `inspect` by 42%. Is field
trimming the biggest lever available, or is there something larger that nobody
here has measured?

**Answer:** field trimming is not the biggest lever. Two larger ones are sitting
in the diagnostics and in a missing rung of the detail ladder, and both are
worth more than the 42%. The fashionable answer — adopt a token-optimised
notation — is measurably a bad trade, and the reason is worth recording so it
does not get proposed again.

Every number below labelled *measured* was measured against the same 61-file
fixture the v0.2.1 plan uses, with the binary at
`3811fbc`. Numbers from published work are attributed.

## 1. What the published work says

### Format substitution is a bad trade

The obvious move for a tool that emits JSON is to stop emitting JSON. Kutschka
and Geiger evaluated exactly that inside agentic tool-calling loops — TOON and
TRON against a JSON baseline, four benchmarks, five open-weight models
(17B–32B), separating input compression from output compression
([arXiv:2605.29676](https://arxiv.org/abs/2605.29676)):

- TRON: up to **27% fewer tokens**, accuracy **within 14 pp** of JSON.
- TOON: up to **18% fewer tokens**, at a similar **9 pp** accuracy cost — and it
  degrades further across turns, where a parse failure "cascades into additional
  reasoning iterations", and it "collapses parallel tool-call output for most
  models".

Two corroborating results in the same review: Matveev found plain JSON has the
best one-shot accuracy — better than constrained decoding — and that the prompt
overhead of *teaching* a model the syntax "can wipe out the per-token savings on
short outputs" ([arXiv:2603.03306](https://arxiv.org/abs/2603.03306)); McMillan
ran 9,649 prompt-completion trials over 11 models and 4 formats and found format
does **not** significantly affect aggregate accuracy (χ²=2.45, p=0.484), with a
21 pp capability gap between model tiers dwarfing any format effect.

**The mechanism, in the authors' own reading, is the useful part:** "Removing
JSON's structural elements (braces, brackets, repeated key strings) drops a
substantial share of tokens that carry no task-relevant signal." That is a
statement about *redundancy*, not about notation. Deleting a field that is never
read removes the same redundancy and costs nothing in familiarity.

*Consequence for ngx:* **do not adopt TOON, TRON or a bespoke notation.** The
best case on offer is 18–27% at a 9–14 pp accuracy cost plus a prompt tax, where
deleting unread fields already returned 42% at zero accuracy cost. And the one
place the papers say tabular encoding compresses hardest — uniform arrays of
identical shape — ngx already serves with `--format table`, in TSV, which needs
no teaching because every model has read a million of them.

### A CLI is already the cheap architecture

Scalekit benchmarked five deterministic GitHub tasks three ways — bash with the
`gh` CLI, bash plus ~800 tokens of usage tips, and GitHub's official MCP server
with 43 tool schemas — on Claude Sonnet 4
([methodology and per-task figures](https://www.scalekit.com/blog/mcp-vs-cli-use)):

| task | CLI | CLI + tips | MCP |
|---|---|---|---|
| repo language | 1,365 | 4,724 | 44,026 |
| PR details | 1,648 | 2,816 | 32,279 |
| repo metadata | 9,386 | 12,210 | 82,835 |

4× to 32× more expensive, and the difference is almost entirely the 43 tool
definitions injected on every turn for the one or two the agent uses. Task
completion went the same way: 25/25 for both CLI variants, **18/25 for MCP**,
every failure a TCP timeout — a class of failure a local binary does not have.

*Consequence for ngx:* **do not ship an MCP server** as a token-efficiency
measure; it is the more expensive and less reliable shape of the same
capability. If one is ever built, the reason will be per-user OAuth, tenancy and
audit trails for a hosted product — governance, not cost. Writing that down now
is the point: "add MCP support" reads like an obvious win, and on cost it is the
opposite.

Note the middle column, which is the uncomfortable one: **prepending 800 tokens
of usage tips made the simple task 3.5× more expensive.** Documentation an agent
reads before it needs it is not free, and ngx has 5,826 bytes of root `--help`
and 4,043 for `get` alone — see finding F4.

### Not returning the data beats compressing it

Anthropic's code-execution work reports a workflow going from **150,000 tokens
to 2,000 — 98.7%** — by loading tool definitions on demand and, crucially,
filtering and aggregating *in the execution environment* so intermediate results
never enter the context: "the agent sees five rows instead of 10,000"
([code execution with MCP](https://www.anthropic.com/engineering/code-execution-with-mcp)).

*ngx already has both halves of this*, and they predate this research: `--query`
runs jq inside the binary, and `--field` returns one scalar. The measured floor
for "which ports are listened on" over 61 files is **302 bytes** with `--query`
against 14,776 for the same answer as JSON. That ratio, 49×, is larger than any
amount of field trimming can reach, and it is already shipped.

*Consequence:* the highest-value work is not making the tree cheaper. It is
making sure the agent can *find the query it needs on the first try* — because
the fallback is reading the tree. See finding F1.

### Levels of detail, lightweight identifiers, and helpful errors

Anthropic's tool-writing guidance
([writing tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents))
converges with v0.2.1 from the other direction:

- a `response_format` enum with `concise` and `detailed` — their example goes
  from 206 tokens to 72 by dropping technical identifiers. `--detail
  answer`/`full` is the same mechanism, with the deliberate difference that ngx
  **keeps** its identifier, because `ref` is what the next command takes;
- resolving "arbitrary alphanumeric UUIDs to more semantically meaningful and
  interpretable language ... significantly improves Claude's precision in
  retrieval tasks" — which is an argument for `ref` being `<file>#<id>` rather
  than a hash, at the cost this project already measured at 19.3% of the answer;
- pagination, range selection, filtering and truncation "with sensible default
  parameter values", and a 25,000-token ceiling on tool responses in Claude Code.
  Measured: `inspect --full-tree` on 61 files is **77,656 bytes**, roughly 19k
  tokens, and a real 133-file production config was measured at 1.6 MB in the
  v0.1 work — **an order of magnitude past that ceiling**;
- errors that "clearly communicate specific and actionable improvements" rather
  than a raw failure.

The context-engineering guidance
([effective context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents))
adds the frame that explains why F3 below matters: an agent should hold
"lightweight identifiers (file paths, stored queries, web links)" and expand
them on demand, because attention is a budget and "as the number of tokens in
the context window increases, the model's ability to accurately recall
information from that context decreases".

## 2. Where ngx actually stands

Measured, 61-file fixture:

| what the caller asks | bytes |
|---|---|
| `inspect --json` (summary: counts only) | 298 |
| `get --directive listen --query '[...args[0]]'` | 302 |
| `inspect --file X --json --detail answer` | 1,244 |
| `inspect --file X --json` (full) | 1,775 |
| `get --directive listen --format table` | 2,626 |
| `get --directive listen --json --detail answer` | 8,176 |
| `get --directive listen --json` (full) | 14,776 |
| `inspect --full-tree --json --detail answer` | 45,402 |
| `inspect --full-tree --json` | 77,656 |

Three things this project already does that the literature recommends, so they
do not get "improved" away:

- **`--format table` is TSV, not a new notation.** The papers' strongest
  compression case, in an encoding no model has to be taught.
- **A filter that matches nothing names the candidates, and caps the list.**
  `--directive listenn` returns NGX-0106 naming the 9 directives in scope (146
  bytes); `--file nope.conf` returns NGX-0102 naming 20 of 61 files and then
  `(and 41 more)` — 523 bytes, bounded, and honest that it was bounded.
- **Output is byte-identical across identical runs** (verified over 6 runs), so
  nothing downstream sees spurious diffs. `meta.duration_ms` is the one field
  that will break this the moment the work takes longer than a millisecond,
  which locally it does not and remotely it will.

## 3. Findings, most valuable first

### F1 — `--query` is the one error in the tool that teaches nothing

An agent that guesses the envelope shape wrong gets 35 bytes:

```
--query: cannot iterate over: null
```

The shape is `data.config[].file`; the guess was `data.files[]`. Nothing in that
message says so. The documented way to learn the shape is to fetch the tree —
**77,656 bytes** — which is the single most expensive call in the tool, provoked
by its cheapest failure.

Every other filter in ngx already handles this: NGX-0102 and NGX-0106 name the
candidates and cap the list. `--query` is simply not following the pattern the
rest of the tool established.

**Proposal:** on a `--query` failure, name the keys available at the path that
failed — `data has keys: config, summary` — capped like the others.

**Value:** roughly +60 bytes on a failure that currently costs a caller up to
77,656 to recover from. It is the highest-leverage change available and it is not
an output-format change at all.

### F2 — an informational diagnostic is 194 bytes of prose the machine reader is forbidden to read

`NGX-0105` rides along on every filtered answer: 142 characters of explanation,
194 bytes with its JSON wrapper — **6.7% of the cheapest table answer** and a
larger share of anything smaller.

This project's own rule is that a caller must never branch on human-readable
text; diagnostics carry a code and the caller switches on the code. So for the
machine consumer, that sentence is by construction unreadable — and charged for
on every call.

**Proposal:** under `--detail answer`, an **info**-severity diagnostic emits its
code and omits the prose. Warnings and errors keep theirs, because those exist to
be relayed to a human, and F1 is the proof that prose in an error earns its
tokens.

**Value:** measured 6.7% of a table answer, more on a small one, on every
filtered call — and it costs the caller nothing, because the code was always the
contract.

### F3 — the detail ladder is missing its cheapest rung, and that rung is worth more than v0.2.1

Between "61 files exist" (298 bytes) and the whole tree (77,656) there is
nothing. An agent that needs to know *which* servers or *which* files must fetch
the tree; `--query` then makes the answer cheap — 1,509 bytes for the file names
— but the tokens are saved after the fact, and over SSH every file was still
read.

Measured, same fixture, whole tree:

| rung | bytes | vs full |
|---|---|---|
| ref + directive only | 12,438 | **-84%** |
| `--detail answer` | 45,402 | -42% |
| full | 77,656 | — |

A `refs` rung is **3.6× cheaper than `answer`** and 6.2× cheaper than full. The
two-step an agent would actually take — list the refs, then expand the one file
that matters — measures **12,438 + 1,244 = 13,682 bytes against 77,656, or
-82%**, which is twice the cut v0.2.1 delivered.

This is the shape the context-engineering guidance describes: hold the
lightweight identifier, expand on demand. ngx has the identifier — `ref` — and
no way to ask for only it.

**Proposal:** `--detail refs`, emitting `ref` and `directive` and nothing else,
on `get` and `inspect`. It is the same mechanism as `answer`, one rung further
down, and it is a strictly additive flag value.

### F4 — the help is the most expensive artefact in the project and has never been measured

Measured: root `--help` **5,826 bytes**, `get --help` 4,043, `inspect --help`
3,899. An agent that reads the root help and one subcommand's help spends
roughly **2,500 tokens before asking anything** — more than the answer to the
commonest question costs in full detail.

The Scalekit column above is the warning: prepending 800 tokens of usage tips
made a simple task **3.5× more expensive**, because the tips were paid for
whether or not they were needed.

The conclusion is not "write a skill" and it is not "trim the help" — a human on
a terminal needs it. It is that **the interface has to be guessable without the
help**, which makes F1 the same finding seen from the other end: the cheapest
documentation is an error message that arrives only when it is needed and says
exactly what to do next.

**Proposal:** no change to `--help`. Treat "could an agent have recovered from
this without reading the help?" as the acceptance question for every diagnostic,
starting with F1.

### F5 — flipping `--detail`'s default is worth more than it looks

Recorded in the v0.2.1 plan as a `schema_version` question. The Scalekit and
code-execution results sharpen it: the callers who never learn a flag exists are
the majority, and they are the ones paying 77,656 bytes today. A default that is
`answer` for `get` and `full` for `inspect` is defensible on the grounds already
in the plan — `get` answers a question, `inspect` is often the input to an edit,
and only the edit needs spans.

## 4. What not to do

| tempting | measured verdict |
|---|---|
| adopt TOON/TRON | -18% to -27% tokens for 9–14 pp accuracy, plus a prompt tax; deleting unread fields already returned -42% for free |
| ship an MCP server for cost | 4–32× more tokens, 72% vs 100% task completion |
| front-load usage tips or a skill | measured 3.5× worse on a simple task |
| replace TSV with something denser | TSV *is* the tabular form the papers favour, and needs no teaching |
| add `omitempty` to `Span`/`Column` | fixes the number, breaks the meaning — `[0,0)` is a real span |

## Sources

- Anthropic — [Writing effective tools for AI agents](https://www.anthropic.com/engineering/writing-tools-for-agents)
- Anthropic — [Effective context engineering for AI agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- Anthropic — [Code execution with MCP](https://www.anthropic.com/engineering/code-execution-with-mcp)
- L. Kutschka, A. Geiger — [Notation Matters: token-optimised formats in agentic AI systems](https://arxiv.org/abs/2605.29676), arXiv:2605.29676
- A. Matveev — [Token-Oriented Object Notation vs JSON](https://arxiv.org/abs/2603.03306), arXiv:2603.03306
- Scalekit — [MCP vs CLI: benchmarking AI agent cost and reliability](https://www.scalekit.com/blog/mcp-vs-cli-use)
