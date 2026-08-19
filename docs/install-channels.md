# Installing ngx, channel by channel

`ngx` is published through several channels, and they are not equivalent.
They differ in one thing that matters more than convenience: **who owns the
binary afterwards**.

A binary installed by `curl | sh` belongs to you, and `ngx update` replaces it
in place. A binary installed by `brew`, `apt`, `dnf`, `apk`, `scoop`, `winget`
or `pacman` belongs to that program — it keeps a database describing the file
it put on disk — and `ngx update` **refuses**, naming the command that does
the job properly. That refusal is deliberate (DC2 in
`docs/superpowers/plans/2026-08-19-ngx-distribution-channels.md`): a tool that
swaps a file behind a package manager's back leaves the manager pointing at
something it no longer knows, and the next upgrade either reverts you in
silence or fails.

The binary knows which channel it came from because the build said so, at link
time. It never guesses from its own path.

## Which of these work today

Only the channels that need nobody's permission are live: `install.sh`, the
tarballs, and the `.deb`/`.rpm`/`.apk` attached to every release. Those cover
Linux, macOS and Windows on their own.

The other four — Homebrew, Scoop, WinGet and the AUR — are **built and idle**.
Every release compiles their artifacts, proves each one carries its own channel
identity, and writes the manifests; the upload is skipped and the release log
says which were skipped and why.

What they wait on is a credential, and nothing else. It is worth being precise
about this, because the opposite is widely assumed: **a tap, a Scoop bucket and
an AUR package have no gatekeeper.** They are repositories you own, and a new
project with no stars can publish to all three today. The popularity rules
people remember — 75 stars, 30 forks, 30 watchers — belong to `homebrew-core`
and to the official Scoop buckets, which is precisely why this project uses its
own tap and its own bucket instead.

WinGet is the one exception, and even there the gate is review, not notability:
a pull request against `microsoft/winget-pkgs`, a CLA, automated validation and
a human moderator.

Until the credentials exist, take `install.sh` or a package.

## The channels

| Channel | Command | Platforms | Self-updates? | Instead, run |
|---|---|---|---|---|
| direct (`install.sh` / `install.ps1`) | `curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh \| sh` | Linux, macOS, Windows | **yes** — `ngx update` | — |
| tarball / `go build` | download from the release page | all | **yes** — `ngx update` | — |
| Homebrew tap *(not live yet)* | `brew install s0beran0/tap/ngx` | macOS only | no | `brew upgrade ngx` |
| `.deb` | `dpkg -i ngx_*_linux_amd64.deb` | Debian, Ubuntu | no | `apt upgrade ngx` |
| `.rpm` | `rpm -i ngx_*_linux_amd64.rpm` | Fedora, RHEL, Oracle Linux | no | `dnf upgrade ngx` |
| `.apk` | `apk add --allow-untrusted ngx_*_linux_amd64.apk` | Alpine | no | `apk upgrade ngx` |
| Scoop *(not live yet)* | `scoop bucket add s0beran0 https://github.com/s0beran0/scoop-bucket && scoop install ngx` | Windows | no | `scoop update ngx` |
| WinGet *(not live yet)* | `winget install s0beran0.ngx` | Windows | no | `winget upgrade ngx` |
| AUR *(not live yet)* | `yay -S ngx-bin` | Arch Linux | no | `pacman -Syu ngx` |

`ngx version --json` always reports `install_channel`, so a script never has to
guess either.

To only *ask* whether something newer exists, `ngx update --check` refuses in a
packaged channel too — and names the command that answers that question
(`brew outdated ngx`, `apt list --upgradable ngx`, `scoop status ngx`, …)
rather than the one that upgrades.

## Why Homebrew is macOS only

What the release publishes is a **cask**, not a formula. GoReleaser deprecated
its formula support, and Homebrew itself routes pre-compiled binaries to casks;
homebrew-core rejects a formula whose only job is to drop a binary in place.
Homebrew on Linux does not install casks, so a Linux user takes `install.sh`,
the `.deb`/`.rpm`/`.apk`, or the AUR.

The tap repository is called `homebrew-tap` and the install command says
`s0beran0/tap`: `brew` strips the `homebrew-` prefix.

The cask strips the macOS quarantine attribute after install. `ngx` is not
signed with an Apple Developer ID and not notarized, so without that step
Gatekeeper would kill the first run with "the developer cannot be verified".

## What is automatic and what is not

Tagging `vX.Y.Z` runs `.github/workflows/release.yml`, which builds, signs and
publishes. From there:

**Fully automatic, every release, no human in the loop:**

- the six tarballs/zips and `checksums.txt` with its minisign signature;
- the `.deb`, `.rpm` and `.apk`, attached to the GitHub release;
- the Homebrew cask pushed to `s0beran0/homebrew-tap` — *if* the token is set;
- the Scoop manifest pushed to `s0beran0/scoop-bucket` — *if* the token is set;
- the AUR `PKGBUILD` pushed to `ngx-bin` — *if* the SSH key is set.

**Not automatic — WinGet.** GoReleaser writes the three manifest files and
opens a **pull request** against `microsoft/winget-pkgs` from a fork. Microsoft
then runs automated validation and a human moderator merges it. So a green
release does not mean "ngx is on WinGet"; it means publication was *requested*.
Expect a lag, and expect to answer review comments on the PR. The first
submission in particular tends to attract manual attention.

**Not automatic — the credentials.** Every one of these channels publishes into
a repository that is *not* this one, and `secrets.GITHUB_TOKEN` reaches only
this one. Each destination needs its own credential, and none of them can be
created by CI. See below.

A missing credential does **not** fail the release. The manifest is still
generated under `dist/`, it is simply not pushed, and the workflow's *report
which channels will publish* step says so in the job summary. That is on
purpose: an absent AUR account must not hold back the tarballs, the packages
and the signature, which have nothing to do with it.

## Setting up the credentials

All four are repository secrets on `s0beran0/ngx`
(*Settings → Secrets and variables → Actions*). Do **not** reuse
`MINISIGN_KEY` or `MINISIGN_PASSWORD` for any of them.

### `HOMEBREW_TAP_TOKEN` — Homebrew tap

1. The repository `s0beran0/homebrew-tap` already exists.
2. Create a **fine-grained** personal access token
   (*Settings → Developer settings → Personal access tokens → Fine-grained*).
3. Resource owner `s0beran0`; **Only select repositories** → `homebrew-tap`.
4. Repository permissions: **Contents: Read and write**. Nothing else.
5. Save the value as the secret `HOMEBREW_TAP_TOKEN`.

A classic token with the `repo` scope also works and is a bad idea: it reaches
every repository the account owns, to update one file in a tap.

### `SCOOP_BUCKET_TOKEN` — Scoop bucket

1. Create the repository `s0beran0/scoop-bucket` (empty is fine; GoReleaser
   creates the manifest on the first release).
2. Same fine-grained token recipe, scoped to `scoop-bucket`, **Contents: Read
   and write**.
3. Save it as `SCOOP_BUCKET_TOKEN`.

One token scoped to both `homebrew-tap` and `scoop-bucket` would also work —
store the same value under both secret names, so that revoking one channel
later does not require editing the workflow.

### `WINGET_PKGS_TOKEN` — WinGet pull request

1. Fork `microsoft/winget-pkgs` into `s0beran0/winget-pkgs`. The fork is
   required: a pull request has to come from a branch of a repository you can
   push to.
2. Fine-grained token scoped to that fork, **Contents: Read and write** and
   **Pull requests: Read and write**.
3. Save it as `WINGET_PKGS_TOKEN`.
4. On the first release after that, watch the PR — Microsoft's validation
   comments on it and the merge is manual.

### `AUR_SSH_KEY` — Arch User Repository

The AUR is not GitHub: it authenticates with SSH only, so this one is a
private key rather than a token.

1. Create an account on <https://aur.archlinux.org>.
2. Generate a dedicated key — not your personal one:
   `ssh-keygen -t ed25519 -C "ngx-release" -f ~/.ssh/aur_ngx`
3. Paste `~/.ssh/aur_ngx.pub` into *My Account → SSH Public Key*.
4. Register the package name by pushing an initial `PKGBUILD` to
   `ssh://aur@aur.archlinux.org/ngx-bin.git`. Pushing to a name nobody owns
   creates it; pushing to one somebody else owns is rejected.
5. Save the **private** key (`~/.ssh/aur_ngx`, whole file including the BEGIN
   and END lines) as the secret `AUR_SSH_KEY`.

The package is `ngx-bin` and not `ngx` because it ships a compiled binary
instead of building from source. That is an AUR naming rule.

## Verifying a channel by hand

The check that matters is not "did it install" but "does it know what it is":

```console
$ ngx version --json | jq -r '.data.install_channel'
homebrew

$ ngx update --json | jq -r '.diagnostics[0].message'
this ngx was installed through homebrew, which keeps track of its own
versions: ... Run `brew upgrade ngx` instead
```

A packaged `ngx` that reports `direct`, or that updates itself, is a bug in the
release — not a convenience. The release workflow proves every channel reaches
a running binary before publishing anything, and proves the generic tarball
stays `direct`.
