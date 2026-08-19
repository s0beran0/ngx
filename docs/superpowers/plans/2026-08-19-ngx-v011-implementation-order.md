# ngx v0.1.1 — Implementation order

**Goal:** deliver everything that still fits inside read-only, in an order
where each step is usable on its own and nothing is built on an unproven base.

**Consolidates:** `2026-08-19-ngx-consumable-output.md` (O1–O4) and
`2026-08-19-ngx-distribution-channels.md` (C1–C5). This document does not
restate their decisions; it orders them and records what depends on what.

## Why 0.1.1 and not 0.2.0

Strict semver would make new commands a minor bump. The roadmap reserves
**v0.2 for mutation** — plan/apply with rollback — and that reservation is
worth more than the letter of the rule: someone reading "v0.2" in this project
should understand "it writes now". Everything here is still read-only, so it
stays in 0.1.x.

*Cost accepted:* a changelog where 0.1.1 introduces `ngx get`, which is unusual
for a patch version. The release notes have to say so plainly instead of
letting the number speak.

## Two independent tracks

Output (O) and distribution (C) touch disjoint files. They can run in parallel
and only meet at the end, in documentation and in the release.

```
track O   O1 --field  →  O2 --summary  →  O3 get  →  O4 human
track C   C1 channel  →  C2 nfpms  →  C3 tap  →  C4 aur/scoop/winget
                                  ↘  both  ↘
                                     C5+docs → release 0.1.1
```

## Order, and why this one

### Phase 1 — `--field` (O1)

Comes first for three reasons, in this order of weight. It is the smallest
change. It applies to **every existing command** at once, because it lives in
the renderer. And it is the tool I will use to verify every step after it —
building `get` while still parsing JSON by hand would be building the cure and
taking the disease.

Deliverable: `ngx --field data.nginx.version status` prints `1.20.1`.

### Phase 2 — `inspect --summary` (O2) ‖ install channel (C1)

Both small, both independent, both parallel-safe: O2 is in `internal/cli`, C1
in `internal/update`.

C1 goes early despite the packaging being late, because it is what makes every
packaging task afterwards a config change rather than a design question. It
also carries the rule with the most consequence in the whole release: a
packaged `ngx` refuses to self-update.

### Phase 3 — `ngx get` (O3) ‖ packages (C2, C3, C4)

The big one, and the flagship of 0.1.1. Its four steps are already ordered in
its own plan, and one of them is worth repeating here because it inverts the
obvious sequence: **read pruning comes last**, after the evaluator is proven by
a property test against `inspect`. Making it read fewer files before it is
proven produces a wrong answer faster.

The packaging tasks run alongside it. They are configuration and container
verification, they do not touch Go code beyond C1, and they are the ones most
likely to stall on something outside the repository — a token for the tap, a
container image that changed. Starting them in parallel means their latency
overlaps the work rather than being added to it.

### Phase 4 — human rendering (O4), documentation (C5), release

Documentation last, on purpose: writing it before `get` exists produces
documentation for a command as imagined, and this project already paid for that
kind of drift — a README claiming four commands when there were five, and a doc
saying the remote path had never touched production the day after it did.

## Verification before the tag

Not new work, the same gate already used for 0.1.0, plus what this release adds:

- `make verify` and the integration suite with the bench up
- the six-platform cross build
- `.deb` and `.rpm` installed **in containers**, checking `install_channel` and
  that `ngx update` refuses
- against the production nginx, read-only: `get` returning a subtree identical
  to the corresponding slice of `inspect`, and `--field` answering without a
  second tool
- the published binary re-validated after the release, as done for 0.1.0

## What does not enter 0.1.1

- homebrew-core, Debian and Fedora official: gated on traction and sponsorship,
  not on engineering
- anything that writes to a `.conf`: that is v0.2, and the line is what makes
  this release safe to hand to anyone
