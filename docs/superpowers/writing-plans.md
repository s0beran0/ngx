# Writing plans and reviewing in this project

Read this **before you write an implementation plan** or ship one
review. It is not code invariant — that's why it is not in `CLAUDE.md`.

## Writing implementation plans

the cost it avoids is noted.These rules come from actual defects in this codebase, not from theory. Each

### Code that depends on a third-party library cannot be written from scratch

If a task integrates an external library, the plan code **needs to be
derived from its source**, not from the memory of how it works. Read the source on
module cache before writing the step, and mention in the brief the file and function that
define behavior.

A plan with complete but incorrect code is worse than a plan with description: the
implementer transcribes faithfully, the tests you wrote together agree
with the code you wrote, and the defect only appears in the review — or in
production.

*Cost avoided:* `Parse` ignored crossplane `payload.Errors`, so a
`.conf` with syntax error produced empty tree with exit 0. The tokenizer
broke on `{` without treating `${var}`, making `ngx` reject any
configuration with variable template. Both went through implementation and only
fell in the review.

### When you can't know, have it investigated instead of guessing

If you cannot determine the correct behavior when writing the plan,
**don't write interim code**. Write a step that tells the implementer to read
the specific source and derive the behavior, with the acceptance criteria
explicit. A brief that says "read `lex.go` and make the tokenizer agree to it
token by token" produces better code than a brief with a plausible tokenizer.

### Tests need an external oracle

A test whose expectation you derived from the same mind that wrote the
implementation only confirms that you were consistent. When there is a reference
independent — the library we are mirroring, the actual nginx binary, a
corpus of known files — test against it.

Particular care with property tests and fuzz: check that the property **can
fail**. Before accepting a fuzz as a safety net, break the implementation
on purpose and confirm that he accuses.

*Cost avoided:* the tokenizer fuzz ran 9.5 million executions and proved
just absence of panic. His four assertions were tautologies — the main one,
`src[Start:End] == Raw`, it is impossible to fail because the code constructs `Raw`
slicing `src[start:pos]` and writes `Start`/`End` to the same expression. The bug
`${var}` survived through this loophole. The Replacement — Compare Token to Token
against `crossplane.Lex` — would have found it in seconds.

### Fix rounds that require new logic are implementation, not fix

The moment of greatest risk is not the initial implementation: it is the correction. One
implementer fixing a finding under pressure invents new mechanism without the
care taken on the first pass, and no one checked that mechanism.

When a fix introduces structure that didn't exist — a cache, a type of error,
a goroutine, a lock — treat the re-review as a complete review, in the most
capable, and ask for explicit judgment on the new logic. Do not use re-review
cheap scopado, which only checks items off a list.

*Cost avoided:* the fix that corrected five findings in `Parse` introduced a date
race and silent file truncation. The truncation was worse than the
original problem: the spans were coherent with a truncated `Source`, and in v0.2
a byte replacement write would truncate the user's real file.

### Never dispatch a loop without stopping condition

Instructions like "run fuzz and fix the cases it finds" or "iterate
until it passes" have no end once the search is opened. Each correction reveals the
next case, and the agent spends hours grinding diminishing returns without
realize, because at every step he is actually finding something real.

Always give an explicit ceiling: a number of rounds, a time limit, or a
sufficiency criterion ("stop when the N listed items are
addressed, even though fuzz still finds cases"). And ask that whatever is left
turn **deliverable** — a list of known divergences with input,
expected and observed behavior — rather than abandoned work.

*Cost avoided:* a tokenizer fix ran 37 minutes and 287 thousand tokens
because dispatch ordered to correct everything that the differential fuzz found. O
finding that justified the round — a bug that caused the CLI to refuse configuration
valid — appeared within the first few minutes; the rest was tail.

### Lock contracts with literal values

When a value is contract — exit code, JSON tag, field name consumed by
another module — the test needs to assert the **literal**, not the symbolic constant.
`require.Equal(t, ExitDrift, CodeOf(err))` passes after someone switches
`ExitDrift` from 7 to 8; `require.Equal(t, 7, int(ExitDrift))` does not.

## Reviewing

- Tell the reviewer what is at stake in that specific task, don't just "review".
  The quality of the find follows the quality of the framing.
- Ask for negative verification: for the reviewer to break the code and confirm that the test
  accuses. A reviewer who reversed `mirroredreading` and measured 8 out of 8 failures
  executions gave more information than the implementer, who reported 2 out of 3.
- Never instruct a reviewer not to flag something. If you think a find would be
  false positive, let it appear and decide later, recording the decision.

## Dispatching subagents without burning context

Each subagent is its own session: it rereads its own history every turn and
does not appear in the `/context` of the coordinator. In a real execution of this project,
subagents consumed around 1.7 million tokens compared to 1.1 million for
reviewers—and the most expensive agent was a Sonnet, not an Opus. **Switch model
it is not the lever; reduce turns and verbose output is.**

### What to write in every dispatch

- **`go test ./...` without `-v` by default.** Use `-v` only when something fails and
  you need the detail. The verbose output of this project's suite goes from
  16 KB; the compact one fits in 300 bytes.
- **Never paste long output into the report.** Report the completion and path of the
  file. Anyone who wants the details opens the file.
- **Short and fixed final report:** STATUS, commits, a test line,
  concerns. The detail goes in the report file, not in the response.
- **Cover any open quest** — number of rounds or time limit —
  and ask what is left to become a list of known divergences. See the rule
  over loops without stopping conditions.

### RTK is active on this machine

`rtk` intercepts shell commands and compresses the output before it enters the
context. It is installed via Homebrew (`homebrew-core`, Apache 2.0) and linked
by a global `PreToolUse` hook.

What does this change in practice:

**No faults
  Exclusion is in
  The review flow
  It helps with `go test`, not everything.are hidden** — verified by breaking a test on purpose: RTK shows
  `~/Library/Application Support/rtk/config.toml`.
generates packages with `git diff a..b > file`, and the proxy would collapse the diff to
  - `go test` is automatically rewritten. `--stat` — leaving the reviewer with filenames and no content. name, file, line and message, and informs the path of the complete output, which
  - **`Read`, `Grep` and `Glob` from Claude Code do not pass through the hook.** How good
  The entire suite reports
  Failure
  it saves to disk.
part of the subagents' consumption is file reading, the real gain here is
  `Go test: 160 passed in 5 packages` instead of 16 KB output. silent that no test would show. - **`git` is excluded from interception**, on purpose. lower than the advertised 60–90%. 

### The cost of a subagent is quadratic in the number of shifts

Measured on actual transcripts from this run, not estimated:

| | |
|---|---|
| initial context of every agent | ~14,900 tokens |
| longest agent final context (232 turns) | ~325,000 tokens |
| average cost of an additional shift | ~114,000 reread history tokens |
| history reread, total | ~131 million tokens |
| content generated by the model, total | ~539 thousand tokens |

the cost does not grow with the number of shifts, it grows with the **square** of it. One
For every token the model wrote, 243 were resent history. 232 shift agent cost 44 million; Each
one of 104 cost 11 million.turn rereads all the accumulated context, and the context grows with each turn — so

**Practical consequence, which is counterintuitive:** prefer **more agents
short** to a long agent. Splitting 232 shifts into two 116 agents costs
around 45% less, even paying the base 15 thousand tokens twice.

or 0.08% of the reread history. Output compression tools attack this
**What is NOT useful:** compressing the output of the tools. slice. All content of
They are not useless, but they are not the lever.combined tool result — Read, Bash, Edit, Write — gives ~245 thousand tokens,

**What's the point, in order:**

1. **Turn ceilings.** Every open search needs a limit. The most expensive agent
   of this execution was the fuzz loop without stopping condition.
2. **Split long task into two dispatches** instead of one, with the second
   receiving state by file instead of by inherited context.
3. **Tell exactly which file to read.** Each turn of exploration costs
   whole context again.
4. **Smaller base** — fewer skills and fewer MCP servers reduce the ~14,900
   every agent, which multiplies by their number.

### The three dispatch rules, and the evidence for each

An empirical study on SWE-bench with Claude Sonnet, GPT-4.1 and Gemini 2.5 Pro
measured turn control strategies (arXiv 2510.16786). The result that
matters: **limiting shifts didn’t make quality worse — it improved.** Budget
tight forces the agent to get straight to the point instead of exploring.

| Strategy | Success rate | Cost |
|---|---|---|
| Fixed limit at the 75th percentile (Claude) | −5,3% | −23,6% |
| **dynamic** limit — starts low, extends once (Claude) | **+1,6%** | −15.6% additional |

Anthropic recommends the same path from another angle: compaction,
external note-taking, isolation in sub-agents and just-in-time loading —
everything architecture and discipline, closing with "do the simplest thing you can
works." No tools are required.

#### 1. Shift budget: the ceiling is a consequence of the scope, not a number

**Nothing applies the ceiling.** Measured here: the tool that launches subagent does not have
budget parameter, and hook `PreToolUse` **does not fire within
subagent** — two probes, registered in the project's `settings.local.json` and
also in the global `~/.claude/settings.json`, 6 tool calls
subagent, zero log entries; Only the coordinator's session appears.
Therefore the ceiling and text in the prompt: a request, never a barrier.

same day:A request is only fulfilled if it is fulfillable. The same agent, same model, in

| dispatch | work items | roof | used |
|---|---|---|---|
| fix round 2 of Task 9 | 4 findings + 1 ruling + 2 optional | 50 | **70** |
| `include.;` addendum | 1 found | 25 | **13** |

The agent did not "disobey" at first: he chose to finish the job instead.
instead of leaving halfway, which was the right decision given what was
request. **When the ceiling and the task contradict each other, the task wins.**

So the rule is about scope, not number:

- **A dispatch defect.** If you are writing the third item in a
  list, break it into two dispatches. That's what made the ceiling worth it.
- **The stop condition must be verifiable, not countable.** Model not
  accurately counts your own calls; Does he know how to tell if the test was successful?
  green. Prefer "stop when the suite passes and commits" to "stop on call
  50".
- **Request the number back from the report.** It does not prevent the explosion, but it makes it
  visible — that's how 70 versus 50 appeared.
- **The only real barrier is who coordinates.** If an agent goes beyond what was expected,
  kill and redispatch with smaller scope; do not extend for convenience.

probe, 30-40. Calibration observed in this project, already with a unitary scope: transcription of
Above 60 the cost per shift has already doubled compared to the beginning.brief code, 20-30; implementation with research, 40-50; review com

#### 2. External note-taking to break the quadratic

context tokens instead of inheriting 300K.When an agent reaches the ceiling, it writes the state to a file and **terminates**.
The continuation goes to a NEW agent, who reads the file and starts with ~15 thousand

This converts a quadratic cost into several linear ones. Two 116 agents
shifts cost around 45% less than a 232, even paying the base two
times. **Do not resume a long agent for convenience** — resuming preserves the
entire context and is precisely what we want to avoid.

#### 3. Just-in-time directed

Tell which file to read. Each turn of exploration repays the accumulated context,
So "find out where X is" costs a lot more than "read X on path/y.go".
When you don't know the way, send a direct call instead.
to let the agent wander.

### The dispatch prompt is reread every turn — cut it

Measured in transcripts: the average dispatch prompt had **896 tokens**, and the
total reread because of them was **1.74 million tokens** — seven times all
that an output compression tool could save. A 1,234 prompt
tokens on a 104 shift agent cost 128 thousand alone.

Most of it was redundant: global restrictions that **are already in CLAUDE.md**
that the agent carries, and requirements that **are already in the brief** that he will read.

**Do not repeat in dispatch what CLAUDE.md already says.** Commits without mention of AI,
comments and output in English, zero CGO, JSON lists like `[]` —
All of this already reaches the agent. Repeating costs each of his turns.

#### Dispatch template (target: 300-400 tokens)

```
<one sentence: what to do and where>

## Escopo
UM defeito/tarefa. Pare quando <condicao verificavel: a suite passar, o teste
X ficar verde> e commite. Referencia de ~N chamadas de ferramenta: se passar
disso sem terminar, pare, grave o estado no relatorio e reporte — nao
continue. Diga no relatorio quantas chamadas usou.

## Arquivos
<exact paths. Say what does NOT need reading.>

## What to do
<only what the brief does not cover, or what changed since it was written>

## Ao terminar
`go test ./... -race` (sem -v). Commite. Relatorio em <caminho>.
Final response: STATUS, commit, one line of test output, one line per item.
```

The “why does this matter” motivates the agent and is sometimes worth it — but it takes a toll
every turn. Use a sentence, not a paragraph.

### Three minor wastes, also measured

**`Write` fails on 16% of calls.** Agents in isolated worktree try
write the report in `.superpowers/` of the main repo, isolation
blocks, and they try again. About 15 shifts lost. **Say no
dispatch to write directly to the copy within the worktree**, without trying to
shared path.

**89 Bash commands repeated by the same agent** — `cd` redone 12 times, the
same fuzz run 9 times. Working directory **persists** between calls
from Bash; say this when the agent needs to navigate. And group checks into one
command only instead of one per turn.

**Bash is the most called tool** (642 versus 238 for Read). As the cost and
per turn, chaining three checks in a single command is worth more than optimizing
what each one prints.

### The biggest bottleneck is the coordination session, not the subagents

Measured in the transcripts of this run:

|  | shifts | reread history | per shift |
|---|---|---|---|
| 41 subagents combined | 1.137 | 131,0 M | 115 thousand |
| the coordination session | 687 | **309,5 M** | **450 thousand** |

**The main session was 70% of the total cost.** Her context started on 22
thousand tokens and reached 844 thousand — and each turn rereads everything.

It costs ~900 thousand tokens alone, because it is reread on each subsequent turn.What inflamed her: **the subagents' reports come back to her in one piece.** A
review that returns 3 thousand analysis tokens, received 300 turns before the end,

extensively.This goes against Anthropic's guidance on sub-agents, which are to return
**condensed summaries of 1,000 to 2,000 tokens** after exploring

#### The rule: the subagent's return is a summary, not the work

Every dispatch must explicitly require:

> Write the full analysis in `<report path>`. As a final answer,
> return a maximum of 15 lines: verdict in one line, each finding as one line
> (severity + title + file:line), and the report path. **Do not repeat
> analysis in the response** — whoever coordinates reads the file if necessary.

The full report continues to exist and continues to be read — on demand,
once, for those who need it. The difference is that it stops being resent every
coordination session shift for the rest of the run.

#### And coordination needs context hygiene

- **`/compact` at the end of each phase**, not when degrading. A summary made of
  A healthy session is better than a messy session.
- **`/clear` when changing subject** — investigate token consumption and execute
  An implementation plan does not need to share context.
- **Do not print entire file in the terminal** to "check". `wc -l`,
  `grep -c` and `tail -5` answer the same question at a fraction of the cost.
