# ngx

A Go CLI that makes nginx operable by AI agents: structured JSON output,
selector-based reading, transactional changes with rollback. Personal project
by Eduardo Benck, open source under MIT.

Design and decisions: `docs/superpowers/specs/`. Implementation plans:
`docs/superpowers/plans/`.

## Two audiences, one tool

`ngx` is used by **AI agents** and by **humans**, and that is not an accident:
output is JSON when stdout is not a terminal, and readable when it is. `--json`
and `--human` force either one. Every new command has to serve both.

Where behaviour diverges, the divergence is deliberate and becomes a safety
rule: `--no-redact` is only accepted on a terminal, because a human debugging
can see the secret and an agent reading the pipe, structurally, cannot even ask
for it.

**Careful with the word "agent".** It shows up in two senses in this project:
the *AI agent* that consumes the output, and `ssh-agent`, an operating system
program that holds SSH keys and has nothing to do with AI. Always write
`ssh-agent` with the prefix; "agent" alone means the consumer. Confusing the
two leads to implementing the wrong thing.

## Everything in this repository is written in English

Code, comments, diagnostic messages, `--help` text, README, `docs/` — including
`docs/superpowers/` — commit messages, and identifiers. No exceptions.

*Why:* this is an open source project. A contributor who does not speak
Portuguese hits the barrier before reading a single line of prose: they hit it
in a stack trace, in a symbol name, in a diagnostic they are trying to branch
on. Half-translated is worse than either extreme, because it teaches nobody
which half to trust.

Code comments carry no accents.

This is **checked, not remembered**: `test/language` fails the suite on
Portuguese in a comment, an identifier or a Markdown file, over everything git
tracks. It exists because the rule was broken the day after it was written, in
four files, by whoever wrote it -- the translation was committed as "finish
translating the repository", which reads as a migration that ended, and nothing
looked afterwards. The check has an allowlist for text that is ABOUT a
Portuguese word, and an allowlist entry that stops matching fails too.

## Conventions

- Go 1.25, zero CGO, static binary.
- **Commit messages never mention Claude, AI or co-authorship.** No
  `Co-Authored-By` trailer, no "Generated with". Authorship is Eduardo's alone.
- No mention of SEA Tecnologia in code, licence or documentation. The
  separation is about ownership and licensing, not about use: SEA uses the tool
  like any other open source.
- Every JSON list serialises as `[]`, never `null` — an agent calling `.length`
  on a null list breaks.
- An unavailable field is omitted, never estimated. Absence is information; a
  wrong number is a lie.
- Never branch on human-readable text. Diagnostics get a class or a code, and
  callers switch on that. Message wording changes; contracts do not. This cost
  a real defect: a `--sudo` hint that only appeared when the message contained
  the word "permissao", and vanished silently the day the project was
  translated.
- No shell `exec`. `exec.Command` with explicit argv.

## Dispatching subagents

**A subagent's return is a summary, never the work.** Every dispatch requires,
in these words:

> Write the full analysis to `<path>`. As your final response, at most 15
> lines: verdict on one line, each finding as one line (severity + title +
> file:line), and the report path. Do not repeat the analysis in the response.

*Why:* the return enters the coordinator's context and is re-read on **every
following turn**. Measured on this codebase: the coordination session consumed
309 million tokens of re-read history against 131 million from 41 subagents
combined — 70% of the total cost — because reports came back whole. A 3,000
token review received early costs close to a million on its own.

**One defect per dispatch.** Nothing enforces a tool-call ceiling: the subagent
tool has no budget parameter, and a `PreToolUse` hook does not fire inside a
subagent (measured — a probe registered both project-locally and globally,
zero entries). The ceiling is a request, and it is only honoured when the task
fits inside it: the same agent overran 70 against a ceiling of 50 with 6 items,
and used 13 against 25 with one. If you are writing the third item in the list,
split it into two dispatches.

Still declare a reference count, a **verifiable stopping condition** ("stop
when the suite passes and commit" — a model does not count its own calls, but
it can read a test result), and ask for the count used in the report, which is
what makes an overrun visible.

Detail, dispatch template and the remaining rules:
`docs/superpowers/writing-plans.md`.

## Writing plans and reviewing

Before writing an implementation plan or dispatching a review, read
`docs/superpowers/writing-plans.md`. It records rules taken from real defects
in this codebase — the most expensive one: code that integrates a third-party
library has to be derived from its source, not written from memory.
