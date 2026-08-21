# ngx v0.2 — mutation, transactionally

The first version that writes to a file somebody else's server reads.

Everything before this was reversible by definition: a wrong answer from a
read-only tool costs a wrong decision, and the file on disk is unchanged. From
here on a defect costs the user's configuration, and possibly their site. That
asymmetry is what shapes every choice below — not caution for its own sake, but
the recognition that the failure modes stop being symmetric.

**Decided with the maintainer, 2026-08-21:**

| Question | Answer |
|---|---|
| How a change is applied | `plan` produces a reviewable artefact, `apply` executes it with rollback |
| Operations | change a directive's value; add and remove a directive; create and delete a `.conf`; reload nginx |
| Remote | **local only.** SSH writing is v0.2.1 |
| Oracles | real `nginx -t` on the bench, round-trip, write fuzz, and diff against the original — all four |

**Prerequisites, both closed in v0.1.2:** Lua blocks are delimited by ngx's own
lexer, so the tree no longer describes structures the server would refuse; and
`Node.Ref` names one node, so an edit has a target that cannot be ambiguous.

---

## Phase 0 — Prove the architecture before writing a single byte

**This phase produces no feature. It exists because the property the whole
design rests on has never been tested.**

The design spec (§D2) says it plainly: *"the spans of all nodes, plus the gaps
between them, reconstitute the file byte by byte. If this property holds, the
token-to-tree marriage of D2 is correct and the surgical edit of v0.2 is safe.
If it breaks, it breaks loud and early, before there is any code in production
that can write."*

That test does not exist. What exists in `FuzzAlignment` is weaker in three
ways, and each gap is a way a write can corrupt a file:

| Tested today | Not tested | What a write does wrong without it |
|---|---|---|
| every non-whitespace byte at **root level** belongs to some root Span | nested levels | a byte inside a `server` belongs to no span, so replacing the span loses it |
| coverage of **non-whitespace** bytes | whitespace and the gaps between spans | indentation and blank lines are not attributed, so a substitution can eat or duplicate them |
| containment and non-overlap | that the concatenation **equals** the source | spans can tile without reproducing the file: a gap counted twice passes both checks |

### 0.1 The reconstitution property

Write `TestSpansAndGapsReconstituteTheFile` and a matching fuzz target. For a
file, walk the tree in document order and emit, alternately, the bytes between
the previous span's end and this span's start, then the span itself. The result
must be `== Source`, byte for byte, with no normalisation of any kind.

*Derive it from the code, not from this description:* read
`internal/config/align.go` and `internal/config/node.go` to see how `Span`,
`HeadSpan` and `ArgSpans` relate, and `internal/config/combine.go` to see why
the combined tree has `Source: nil` and must be excluded from this property.

**Acceptance:** the property runs over `internal/config/testdata/*.conf`, over
`test/bench/testdata/lua_surface.conf`, and as a fuzz target for at least 5
minutes without a finding. **And it must be proven able to fail**: break one
span by one byte on purpose and confirm the test accuses. A property test whose
failure was never observed is a tautology — this project has four recorded
instances of checks that could only ever pass, and this is the one where that
mistake writes to a `.conf`.

### 0.2 The gap inventory

The reconstitution property will find gaps that are legitimate — `FuzzAlignment`
already documents one at `fuzz_test.go:130`. Each one must become an enumerated
entry with the input that produces it, not a loosened assertion.

**Stopping condition:** three rounds of "run, enumerate, re-run", or a round
that finds nothing new. Whatever is left becomes a list of known gaps with
input, expected and observed — not abandoned work.

---

## Phase 1 — The plan artefact

`plan` is the whole safety design. It is what makes `apply` mechanical.

### 1.1 What a plan is

A JSON document, in the same envelope as everything else, whose `data` holds an
ordered list of **edits**. An edit is a byte substitution in one file:

```
{
  "file": "/etc/nginx/conf.d/site.conf",
  "ref": "/etc/nginx/conf.d/site.conf#s0.d0",
  "span": {"start": 34, "end": 46},
  "before": "listen 8080;",
  "after": "listen 8443 ssl;",
  "reason": "set listen"
}
```

`before` is not redundant with `span`. It is what makes `apply` able to refuse:
if the bytes at that span are not `before`, the file changed since the plan was
built, and applying it would cut in the wrong place. That is the check, and it
is per-edit rather than per-file.

### 1.2 Two anchors, because one is not enough

- `meta.config_hash` — the whole configuration as it was read. `apply` refuses
  a plan whose hash does not match, with **exit 9** (`ExitHashMismatch`, defined
  in `output/errors.go:20` and, as of v0.1.2, never used — this is its first
  use).
- `before` per edit — catches the case the hash cannot: a file the plan does not
  touch changed, the hash differs, and refusing the whole plan is right; but
  also a file the plan DOES touch changed in a way that happens to preserve the
  hash, which `before` catches and the hash does not.

*Design note to settle during implementation:* the hash covers base names, not
absolute paths (`hash.go:20-24`). Decide, and record, whether a plan built
against `/etc/nginx` may be applied against a copy elsewhere. The conservative
answer is no, and it needs an anchor the hash does not currently provide.

### 1.3 A plan never contains a rendered file

An edit is a span and replacement bytes. It is never "here is the new content
of the file", because that turns every apply into a whole-file rewrite and
loses the guarantee D1 exists for: formatting outside the edited span is not
merely preserved, it is **never read into the write path**.

**Acceptance:** a test asserting that no field of a plan holds more bytes than
the edit itself. That is the structural version of "we do not rewrite files".

---

## Phase 2 — Writing one file, atomically

### 2.1 The write, and why the order is the whole thing

For each file with edits, in one pass:

1. Read current bytes. Compare each edit's `before` against its span; any
   mismatch aborts the entire apply **before anything is written**.
2. Build the new content by substitution, applying edits from the highest offset
   to the lowest so earlier offsets stay valid.
3. Write to a temporary file **in the same directory**, `fsync` it, and
   `os.Rename` over the target.

Same directory because rename is only atomic within a filesystem. `fsync`
before rename because a rename that lands before the data does gives a
zero-length config after a power loss — the failure mode is not "old content",
it is "no content".

*Derive from source:* read `internal/update/update.go`, which already does
exactly this dance for the binary — temporary file in the same directory, then
rename — and reuse its reasoning rather than reinventing it. The comment at the
top of that file states the invariant.

### 2.2 Permissions and ownership are part of the content

A `.conf` written with the wrong mode is a defect even when the bytes are
right, and a root-owned file rewritten as the invoking user is worse: nginx may
still read it, and the next `apply` may not.

Read the target's `os.Stat` before writing and apply mode, uid and gid to the
temporary file before the rename. **Acceptance:** a test that writes to a file
with mode `0600` owned by another uid (skipped when not running as root) and
asserts all three survive.

### 2.3 Rollback

Keep the original bytes in memory for every file touched by the apply. After
all writes land, run `nginx -t`. If it fails, restore every file — by the same
temp-plus-rename path — and exit non-zero with the `nginx -t` output attached.

**Two things this must not do.** It must not restore a file it never wrote, and
it must not report success when the restore itself failed: that is the state
where the operator has to be told exactly which files are in which state, in a
machine-readable form. A partially restored configuration reported as "rolled
back" is the worst output this tool can produce.

**Acceptance:** a test that makes `nginx -t` fail *after* a successful write —
by planning a syntactically valid but semantically invalid change, such as a
`listen` on a port already bound in another server — and asserts the file is
byte-identical to the original afterwards.

---

## Phase 3 — The operations

Each is a plan producer. None of them writes.

### 3.1 `set` — change a directive's value

The lowest-risk operation, and the one the spans already support:
`HeadSpan` is name plus arguments, so `set` replaces the arguments and leaves
the terminator, the comments and the block alone.

Careful with two shapes that already have tests: `HeadComments` (a comment
*inside* the head, `node.go:34-41`) and `ArgSpans` for `if`, reported as
unavailable because crossplane rewrites `Args`. `set` must refuse the second
rather than guess. `--ref` is the only way to name the target.

### 3.2 `add` and `rm` — a directive appears or disappears

This is where indentation is decided, and where a naive implementation produces
a diff nobody wants to review. Rules to derive from the file rather than from
defaults:

- indentation copied from the sibling directives of the target block, not from a
  fixed width;
- line ending copied from the file (a CRLF file stays CRLF — the tokenizer
  already keeps the CR out of comment spans for exactly this reason);
- `rm` takes the directive's whole line including its trailing newline, and
  leaves surrounding blank lines untouched.

**Acceptance:** for a corpus of real files, `add` then `rm` of the same
directive returns the file **byte-identical** to the original. That round trip
is the oracle, and it is stronger than any expectation written by hand.

### 3.3 `touch` and `rm` for files

Creating a `.conf` in `conf.d/` is only useful if nginx loads it, so this
operation has to answer the `include` question: verify a glob already covers the
new path, and refuse with a clear diagnostic when it does not, rather than
writing a file that does nothing. Deleting is the reverse and must state how
many `server` blocks disappear with it.

### 3.4 `reload`

The only operation that touches the running server. `nginx -t` first, always,
even if `apply` just ran it — the configuration may have changed in between, and
this is cheap. It is a separate command from `apply` on purpose: applying and
reloading are different decisions, and a tool that couples them cannot express
"stage this now, reload in the maintenance window".

---

## Phase 4 — Privileged writing

**This is the phase most likely to be underestimated, and it needs a decision
before code.**

Reading with privilege works today through `sudo -n cat -- <file>`
(`transport/privileged.go:165`). Writing has no equivalent, because
`Transport.Run` has **no stdin** (`transport.go:37`), and the project forbids
shell `exec` — so `sudo -n tee -- <file> < tmp` is not available as written.

Three options, to be settled with the maintainer:

1. **Extend `Transport.Run` with stdin** and write via `sudo -n tee -- <path>`.
   Smallest surface, but `tee` truncates in place: an interrupted write leaves a
   partial file, which is exactly what the temp-plus-rename dance exists to
   prevent.
2. **Write to a temporary file as the user, then `sudo -n mv`.** Keeps
   atomicity, needs the temp file to be in a directory the user can write and
   `mv` to cross into root-owned territory — which changes ownership semantics.
3. **Refuse privileged writes in v0.2** and document that the user must own the
   files, as `docs/install-channels.md` already recommends for reading.

Option 3 is the honest default for a first write-capable release. Whichever is
chosen, the reasoning goes in the plan before the code.

---

## What is deliberately NOT in v0.2

- **Remote writing.** Decided: v0.2.1. A dropped connection mid-write is a
  distinct failure class and deserves its own design, not a corner of this one.
- **`fmt`.** Listed in the v0.1 spec roadmap and never built. It is a
  whole-file rewrite, which contradicts §1.3, and it needs its own justification.
- **`lint`.** v0.3, and the natural home for the one Lua divergence still open
  (issue #1): an empty `content_by_lua_block` will not load, which is a finding
  about what the configuration does, not about whether it can be read.

## Release gate

The v0.1.2 gate, plus:

- the reconstitution property (0.1) green as a test and as a 5-minute fuzz, and
  **observed failing** when a span is broken on purpose;
- for every operation, `add`/`rm` and `set`/`set-back` round trips returning the
  file byte-identical, over the syntax-surface fixtures and the Lua surface;
- every write validated by real `nginx -t` in the bench container, not by a
  parser;
- a diff test per operation: the unified diff against the original contains
  **only** the intended lines — no line-ending change, no reindentation, no
  reordering;
- a rollback test where `nginx -t` fails after a successful write;
- write fuzz: random mutations against random fixtures, asserting the file is
  either byte-identical to the original or exactly the intended change, never
  truncated and never touched outside the target span. **Both fuzz targets**,
  since `make fuzz` running one while CI ran two was itself a defect in v0.1.2;
- the agent test, extended: starting from `ngx --help` alone, change a port,
  verify it, and roll it back.

## The rule this plan is written under

Every step above that touches a third-party library says *derive it from the
source* rather than giving code. That is not vagueness. It is
`docs/superpowers/writing-plans.md`, first rule, and the cost it records is
concrete: `Parse` once ignored crossplane's `payload.Errors`, so a `.conf` with
a syntax error produced an empty tree and exit 0. A plan with complete but
wrong code is worse than a plan with a description, because the implementer
transcribes it faithfully and the tests agree with it.
