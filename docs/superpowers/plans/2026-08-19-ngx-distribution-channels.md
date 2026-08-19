# ngx — Distribution channels and the self-update conflict

**Goal:** put `ngx` within reach of as many people as possible, without the
self-updater fighting whoever installed it.

**Spec:** `docs/superpowers/specs/2026-08-17-ngx-cli-design.md`.
**Depends on:** the distribution plan
(`2026-08-17-ngx-distribution.md`), already executed: signed releases,
`install.sh`/`install.ps1` and `ngx update`.

## The problem this plan exists for

`ngx update` replaces its own binary. That is correct when the user installed
it with `curl | sh`, and wrong in every other case: a binary installed by
`apt`, `dnf` or `brew` belongs to the package manager, and swapping it behind
their back leaves the manager's database describing a file that is no longer
there. The next `apt upgrade` either reverts the user silently or fails.

Homebrew states the rule explicitly in *Acceptable Formulae*: a formula must
avoid "self-updating behavior that conflicts with Homebrew's version
management". So this is not only a courtesy to users — it gates acceptance.

## Decisions

### DC1 — The install channel is a build-time fact, never a guess

The binary knows how it was installed because whoever built it said so, via
`-ldflags -X`. It does not try to infer it from its own path, from the
presence of `/usr/bin/dpkg`, or from file ownership.

*Why:* every inference is wrong somewhere. A binary under `/usr/local/bin` may
have come from `curl` or from a tap; `dpkg` exists on machines where `ngx` was
installed by hand; a container has no package manager and is not "direct". A
guess that is right most of the time produces a rare, confusing failure — and
this decision is about the case where the tool would corrupt the state of
another program.

*Consequence:* the default value is `direct`, because that is what a build
from source is. Someone building `go build ./cmd/ngx` gets a working
self-updater; every packaged build overrides it.

### DC2 — Refuse and teach, never work around

With a channel other than `direct`, `ngx update` **does not update**. It exits
with a typed diagnostic naming the right command for that channel — `brew
upgrade ngx`, `apt upgrade ngx`, `dnf upgrade ngx`. It does not fall back, does
not ask, does not offer `--force`.

*Why:* this is the same rule as `--sudo` in DR5 and as the installer refusing
an unverifiable signature. A tool that finds a way around the constraint it was
given teaches the user that the constraint is decorative.

### DC3 — `--check` refuses too, but answers the question that was asked

In a packaged channel `--check` does not report the newest GitHub release. That
release is not what the package manager is able to install, so reporting it
would invent an update the caller cannot apply — a wrong answer is worse than a
refusal, because the caller cannot tell it is wrong.

But a bare refusal answers the wrong question. `--check` asked "is there
anything newer?", not "update me", so the refusal names the command that asks
**that**: `brew outdated ngx`, `apt list --upgradable ngx`, `dnf check-update
ngx`. Sending someone to `brew upgrade` when they only wanted to know turns a
refusal into a dead end.

*Also decided:* an empty channel counts as unknown and refuses. The default is
`direct`, so empty can only come from `-ldflags -X` with no value — which is
exactly the silent-failure mode this project already met once with the public
key. Failing closed there costs a packager one clear error; failing open costs
a user a corrupted installation.

### DC4 — `redacted_args`, and the contract break it carries

A redacted value and a literal `***` in the configuration were
indistinguishable: both came out as `"***"`. An agent that cannot tell
censorship from content either retries in a loop or reports the key as empty.
Nodes now carry `redacted_args`, the indices of the arguments that were
replaced, omitted when there are none.

Making the indices mean anything required redacting **per argument** instead of
collapsing the whole list. That is a better answer — `["Authorization", "***"]`
keeps the header name that `["***"]` threw away — and it is a **breaking
change** for anyone reading `args[0]` of a redacted directive.

*How it is handled:* v0.1.0 declares no `schema_version`, so the field arriving
with value **1** in 0.1.1 marks exactly this line. Anything without the field is
pre-contract. The release notes have to state the break in words, because a
consumer that never reads the schema version will not notice it any other way.

### DC3 — Reach comes before officialdom

Channels that depend on nobody's approval come first: `.deb`/`.rpm`/`.apk`
attached to the release, our own Homebrew tap, AUR, Scoop. The official
repositories — homebrew-core, Debian, Fedora — come later, if ever.

*Measured, 2026-08-19:* homebrew-core requires, for a **self-submission by the
repository owner**, at least **90 forks, 90 watchers or 225 stars**, against
30/30/75 when someone else submits, and the repository has to be at least **30
days old**. `ngx` is two days old with no traction, so the tap is not a
consolation prize — it is the only Homebrew path that exists today.

Debian is the heaviest: an ITP bug, a Debian Developer to sponsor it, and every
Go dependency packaged in Debian or vendored with justification. `ngx` depends
on crossplane, koanf, cobra, x/crypto, sftp, ssh_config, go-winio and minisign.
Not a first step.

---

### Task C1: The install channel in the binary

**Files:**
- Modify: `internal/update/update.go` — `InstallChannel` variable and the refusal
- Modify: `internal/cli/update.go` — surface the refusal
- Modify: `internal/cli/root.go` — `ngx version` reports the channel
- Test: `internal/update/update_test.go`, `internal/cli/update_internal_test.go`

- [ ] **Step 1: Write the test first**

A table with one case per channel: `direct` updates; `homebrew`, `deb`, `rpm`,
`aur`, `scoop`, `winget` all refuse. The refusal case asserts **three** things:
the typed code, that the message names the command for that channel, and that
the binary on disk was not touched. Only the third one actually proves the
update did not happen — the other two would pass on a bug that refused with
one hand and swapped with the other.

Also: an unknown channel value refuses. A typo in a packager's build flag must
not silently re-enable self-update.

- [ ] **Step 2: Implement**

`var InstallChannel = "direct"` and a table from channel to upgrade command.
The refusal is an `*output.Error` with its own code in the 03xx range, which is
the update family.

- [ ] **Step 3: `ngx version` reports it**

`install_channel` in the envelope, always present — this one is never omitted,
because "I do not know how I was installed" is not a state that can exist: the
value has a default.

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/update/ ./internal/cli/ -race`

### Task C2: `.deb`, `.rpm` and `.apk` on every release

**Files:** Modify `.goreleaser.yaml`

- [ ] **Step 1: `nfpms` section**

Formats `deb`, `rpm`, `apk`. Binary in `/usr/bin/ngx`, licence in
`/usr/share/doc/ngx/`. Each format overrides `InstallChannel` — `deb` for the
.deb, `rpm` for the .rpm.

**Careful:** the `ldflags` under `builds` are shared by every artifact, so the
channel cannot go there. It needs a build per channel, or a post-processing
step. Read the goreleaser documentation for the version pinned in the workflow
before writing this — do not copy an example from an older major.

- [ ] **Step 2: Prove the packages install**

In a container, not on this machine: `docker run --rm -v $PWD/dist:/d
debian:12 sh -c 'dpkg -i /d/*.deb && ngx version'`, and the equivalent with
`fedora:41` and `rpm -i`. The check is that `install_channel` comes out as
`deb`/`rpm` and that `ngx update` refuses — a package that installs and
self-updates is worse than no package.

### Task C3: Homebrew tap

**Files:** new repository `s0beran0/homebrew-tap`; Modify `.goreleaser.yaml`

- [ ] **Step 1: Create the tap**

Repository `homebrew-tap` with a `Formula/` directory. The name matters:
`brew` derives `s0beran0/tap` from `homebrew-tap`.

- [ ] **Step 2: `brews` section in goreleaser**

Publishing needs a token with write access to the tap repository —
`secrets.GITHUB_TOKEN` does not reach another repository. Create a fine-grained
PAT limited to that repository and store it as a secret. **Do not** reuse the
minisign secret for this.

- [ ] **Step 3: Verify from the outside**

`brew install s0beran0/tap/ngx` on a clean machine, then `ngx version` showing
`install_channel: homebrew` and `ngx update` refusing with `brew upgrade`.

### Task C4: Windows and Arch

- [ ] `scoops` and `winget` in goreleaser; AUR via `aurs`. All self-service.
- [ ] winget requires a PR to `microsoft/winget-pkgs` with automated
      validation; the others publish without review.

### Task C5: Documentation

- [ ] README: an install table by channel, with `curl | sh` first because it is
      the one that works everywhere today.
- [ ] Say plainly that a packaged `ngx` does not self-update, and which command
      to use instead. Someone who runs `ngx update` and gets a refusal must find
      that sentence in the README before deciding the tool is broken.

## Verification

| Requirement | Task |
|---|---|
| Reach the most people | C2, C3, C4 |
| Self-update does not fight the package manager | C1 |
| Homebrew acceptance rule respected | C1, C3 |
| The refusal teaches the right command | C1, C5 |

## What this plan does not cover

homebrew-core, Debian and Fedora official. They are gated on traction and on
sponsorship, not on engineering, and doing them now would be optimising for a
user who does not exist yet.
