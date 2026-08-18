# ngx — Distribution Plan: CI, releases, installation and auto-update

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish `ngx` so that an operator can install it with one command, update it with `ngx update`, and can verify that the binary they received is the one published.

**Architecture:** GitHub Actions runs the suite on all PR and push; a tag triggers goreleaser, which compiles for four platforms, generates `checksums.txt` and signs it with minisign. The public key is embedded in the binary, so `ngx update` checks signature and checksum before replacing itself. Channels leave semver: clean tag is stable, tag with pre-launch suffix is beta.

**Tech Stack:** GitHub Actions, goreleaser v2, minisign, `aead.dev/minisign` v0.3.0.

**Spec:** `docs/superpowers/specs/2026-08-17-ngx-cli-design.md` (§10 Repository and distribution)

**Prerequisite:** Plan 1 needs to be completed — in particular Task 14, which runs the ultimate `go mod tidy`. This plan assumes a stable `go.mod`.

## Global Constraints

- Go module: `github.com/s0beran0/ngx`. Go 1.25.
- **Zero CGO.** Every build uses `CGO_ENABLED=0`. Any new dependencies must be pure Go — check before adding.
- MIT License in the name of Eduardo Benck. No mention of SEA Tecnologia.
- **Commit messages never mention Claude or IA.** No `Co-Authored-By` trailer, no "Generated with".
- Code comments in Portuguese, without accents.
- Platforms: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`,
  `windows/amd64`, `windows/arm64`.
- Distribution archive: `.tar.gz` on Linux and macOS, `.zip` on Windows.

## Decisions

Taken before writing this plan; they are premises for the rest.

### DD1 — Channels by semver, not by branch

Tag `v0.2.0` is stable. Tag `v0.2.0-beta.1` or `v0.2.0-rc.1` is pre-release, marked as such on GitHub. goreleaser does this itself with `prerelease: auto`, which inspects the tag suffix.

*Why:* It does not require keeping two branches in sync or backporting patches between them. That's what most of the Go ecosystem does, so the behavior doesn't surprise anyone.

### DD2 — Checksum verification plus minisign signature

goreleaser generates `checksums.txt` with the SHA256 of each artifact and signs it with minisign, producing `checksums.txt.minisig`. The public key is embedded into the binary at compile time. `ngx update` checks the signature of `checksums.txt`, then checks the SHA256 of the downloaded file against it, and only then replaces the binary.

*Why:* `ngx` runs as root on servers serving traffic. A self-update without verification turns any distribution chain compromise into running code as root on every server you update. Just a checksum is not enough: whoever manages to publish a release would publish the checksum of the binary itself along with it. The signature protects even if the GitHub account is compromised, because the private key lives outside of it.

*Cost accepted:* there is a private key to keep. Losing it means that existing updates stop accepting new releases until a binary with the new key is distributed via another route.

### DD6 — The release proves that the key was embedded, before publishing

Injecting value with `-ldflags -X` **fails silently** if the target variable does not exist with the exact name and type: the linker does not warn, the build passes, and the binary exits with the empty variable. In a signed release this means publishing an `ngx` that cannot verify any signature — the protection disappears without any signal, which is the worst possible failure mode for a security mechanism.

Then the release workflow **checks the built artifact** before publishing: executes the binary and confirms that the embedded public key is not empty. If it is, the release aborts.

*Why:* the alternative is to depend on someone remembering to check. A security control that relies on human memory has already failed; the only question is when.

### DD3 — Public key is embedded, not downloaded

A public key that `update` himself downloads doesn't protect against anything: whoever controls the server hands over his key along with his binary. It is entered via `-ldflags -X` in the build.

### DD4 — Windows is supported, with documented caveats

`ngx` compiles and runs on Windows, because nginx for Windows exists and is officially distributed. But nginx.org itself classifies that build as **beta**: it only uses `select()`/`poll()`, "high performance and scalability should not be expected", only one worker actually works, and there is no support for UDP or QUIC.

*Practical consequence:* the nginx binary on Windows is not installed by the package manager — it is left in an unpacked directory, like `C:
ginx-1.31.3
ginx.exe`, and uses the execution directory as a prefix. So the automatic path detection that works on Linux does not apply, and the documentation needs to show how to point `ngx` to the right directory with `-c`.

*The README tells the user this.* Supporting the platform and being honest about its limitations are not contradictory things — omitting would lead someone to bet production on a build that the vendor itself does not recommend.

### DD5 — On Windows, running binary is renamed, not overwritten

Windows crashes the running executable: renaming works, deleting does not. Windows `Apply` renames the current binary to `.old`, puts the new one in place, and removing `.old` is left to the next run of `ngx`, which does it at startup.

*Why:* the atomic `rename` logic that works on Linux and macOS would fail on Windows, and the failure mode would be an update that aborts in the middle, leaving the user without a working binary. It's not a portability detail — it's the difference between updating and breaking the installation.

---

### Task D1: Continuous integration

- Test: the workflow itself, verified in a push**Files:**
- Create: `.github/workflows/ci.yml`

- Produces: a `ci` workflow that runs on every push to `main` and on every pull request**Interfaces:**
- Consumes: `go.mod`, the Plan 1 test suite

- [ ] **Step 1: Write the workflow**

Create `.github/workflows/ci.yml`:

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      - name: vet
        run: go vet ./...

      - name: testes com race detector
        run: go test ./... -race

      # The binary is distributed statically: a dependency that requires code
      # it would break the cross-compile, and the error would only appear in the release.
      - name: build sem cgo
        env:
          CGO_ENABLED: 0
        run: go build ./...

  cross:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        goos: [linux, darwin, windows]
        goarch: [amd64, arm64]
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      - name: build for ${{ matrix.goos }}/${{ matrix.goarch }}
        env:
          CGO_ENABLED: 0
          GOOS: ${{ matrix.goos }}
          GOARCH: ${{ matrix.goarch }}
        run: go build -o /dev/null ./cmd/ngx
```

- [ ] **Step 2: Check that the workflow is valid**

Run: `gh workflow view ci` after push, or validate locally with `act -n` if available.
Expected: the workflow appears and both jobs are recognized.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: roda vet, testes com race e cross-compile"
```

---

### Task D2: Release by tag, with channels and subscription

- Modify: `internal/output/envelope.go` — expose public key variable to `-ldflags`**Files:**
- Create: `.goreleaser.yaml`, `.github/workflows/release.yml`

**Interfaces:**
- Consumes: `output.Version` (Plan 1, Task 1)
- Produces: `output.PublicKey` (string, filled in at build); release artifacts `ngx_<version>_<os>_<arch>.tar.gz`, `checksums.txt`, `checksums.txt.minisig`

- [ ] **Step 1: Generate the minisign key pair**

This step is done **once, by the repository owner**, outside CI:

```bash
minisign -G -p ngx-minisign.pub -s ngx-minisign.key
```

Keep the private key and password in a safe place, and add two secrets to the GitHub repository: `MINISIGN_KEY` with the contents of the `.key` file, and `MINISIGN_PASSWORD` with the password. The public key (`ngx-minisign.pub`) is versioned in the repository and embedded in the binary.

If the implementer does not have access to the secrets, he stops here and reports — do not invent a key.

- [ ] **Step 2: Expose the public key variable**

In `internal/output/envelope.go`, next to `Version`:

```go
// PublicKey is the minisign public key used to verify releases.
// Filled in at build via -ldflags; empty in local build, which makes the
// update command refuses to update instead of accepting without verification.
var PublicKey = ""
```

- [ ] **Step 3: Write the goreleaser configuration**

Create `.goreleaser.yaml`:

```yaml
version: 2
project_name: ngx

before:
  hooks:
    - go mod tidy

builds:
  - main: ./cmd/ngx
    binary: ngx
    env:
      - CGO_ENABLED=0
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X github.com/s0beran0/ngx/internal/output.Version={{ .Version }}
      - -X github.com/s0beran0/ngx/internal/output.PublicKey={{ .Env.NGX_PUBLIC_KEY }}

archives:
  - formats: [tar.gz]
    # Windows receives .zip: and what the platform opens without extra tool, and
    # what install.ps1 expects.
    format_overrides:
      - goos: windows
        formats: [zip]
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    files:
      - LICENSE
      - README.md

checksum:
  name_template: checksums.txt
  algorithm: sha256

# Assinar apenas o checksums.txt e suficiente: ele cobre todos os artefatos
# by hash, then a signature protects the entire set.
signs:
  - id: minisign
    cmd: minisign
    args: ["-S", "-s", "{{ .Env.MINISIGN_KEY_FILE }}", "-m", "${artifact}", "-x", "${signature}"]
    signature: "${artifact}.minisig"
    artifacts: checksum
    stdin: "{{ .Env.MINISIGN_PASSWORD }}"

release:
  # Marks the release as pre-release when the tag has a pre-release suffix
  # (-beta, -rc, -alpha). And what separates the beta channel from the stable channel.
  prerelease: auto

changelog:
  sort: asc
  filters:
    exclude:
      - "^docs:"
      - "^test:"
      - "^chore:"
```

> The exact syntax of `signs.args` for minisign needs to be confirmed against the goreleaser v2 documentation and `minisign -h` of the version installed in the runner. Run a test release with `--snapshot --clean` locally before creating the first tag, and adjust if minisign complains about the arguments. Don't guess: the error would only appear in the first real release.

- [ ] **Step 4: Write the release workflow**

Create `.github/workflows/release.yml`:

```yaml
name: release

on:
  push:
    tags: ["v*"]

permissions:
  contents: write

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      - name: instala o minisign
        run: |
          sudo apt-get update
          sudo apt-get install -y minisign

      - name: prepara a chave de assinatura
        env:
          MINISIGN_KEY: ${{ secrets.MINISIGN_KEY }}
        run: |
          umask 077
          printf '%s' "$MINISIGN_KEY" > "$RUNNER_TEMP/minisign.key"

      - uses: goreleaser/goreleaser-action@v6
        with:
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          MINISIGN_PASSWORD: ${{ secrets.MINISIGN_PASSWORD }}
          MINISIGN_KEY_FILE: ${{ runner.temp }}/minisign.key
          NGX_PUBLIC_KEY: ${{ vars.NGX_PUBLIC_KEY }}
```

`NGX_PUBLIC_KEY` is a **variable** from the repository (not secret — it is public by definition), containing the key line from the `.pub` file, without the header comment.

- [ ] **Step 5: Test on snapshot before any tag**

Run: `goreleaser release --snapshot --clean --skip=sign`
Expected: generates `dist/` with the four binaries and `checksums.txt`. Confirm with `file dist/ngx_linux_amd64_v1/ngx` that it is static and without a dynamic interpreter.

- [ ] **Step 6: Commit**

```bash
git add .goreleaser.yaml .github/workflows/release.yml internal/output/envelope.go ngx-minisign.pub
git commit -m "release: goreleaser com canais por semver e assinatura minisign"
```

---

### Task D3: Installation script

- Test: `install_test.sh` (runs the script against a temporary directory)**Files:**
- Create: `install.sh`

**Interfaces:**
- Consumes: the artifacts published by Task D2
- Produces: `install.sh`, executable via `curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh | sh`

- [ ] **Step 1: Write the script**

Create `install.sh`. Requirements that the script must satisfy, and that the Step 2 tests verify:

- Detect system and architecture via `uname -s` and `uname -m`, mapping to the names that goreleaser uses (`x86_64` → `amd64`, `aarch64`/`arm64` → `arm64`). Refuse unsupported combination with clear message.
- Resolves the latest **stable** release by default, querying `https://api.github.com/repos/s0beran0/ngx/releases/latest` — this endpoint already excludes pre-releases. Accepts `NGX_CHANNEL=beta`, which starts listing `/releases` and takes the first entry, and `NGX_VERSION=v0.2.0` for fixed version.
- Download the tarball, `checksums.txt` and check SHA256 before extracting. Use `sha256sum` or `shasum -a 256`, whichever exists.
- Installs to `/usr/local/bin` by default, respecting `NGX_INSTALL_DIR`.
- **Checks write permission BEFORE downloading anything**, and if it is missing, aborts with the exact instruction — without calling `sudo` alone. A script that escalates privilege on its own is exactly what no one should run via `curl | sh`, and checking first avoids wasting the download and failing at the end:

```sh
if [ ! -w "$NGX_INSTALL_DIR" ]; then
    echo "erro: sem permissao de escrita em $NGX_INSTALL_DIR" >&2
    echo "" >&2
    echo "rode a instalacao com privilegio:" >&2
    echo "  curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh | sudo sh" >&2
    echo "" >&2
    echo "ou instale num diretorio seu, sem privilegio:" >&2
    echo "  curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh | NGX_INSTALL_DIR=\$HOME/.local/bin sh" >&2
    exit 1
fi
```

Both outputs matter: anyone on a machine where they don't have root needs the second as much as anyone who does need the first.
- Uses `set -eu`, clears the temporary directory with `trap`, and works in pure `sh` — does not assume bash.

> Minisign signature verification is **not** included in the script: it would require minisign installed before installation. The checksum protects against corrupted downloads and the origin is HTTPS from GitHub. Anyone who wants a strong guarantee downloads it manually and checks it, or installs it once and uses `ngx update` from then on, which checks the signature. Document this difference in the README.

- [ ] **Step 2: Write the script test**

Create `install_test.sh`, which exercises the script without touching the system:

- Installs to `NGX_INSTALL_DIR` pointing to a temporary directory and confirms that the binary appears there and responds to `ngx version`.
- Confirms that unsupported architecture fails with non-zero code and message mentioning the platform.
- Confirm that a divergent checksum aborts the installation: download, corrupt the tarball and check that the script refuses.
- Confirms that `NGX_VERSION` fix actually installs that version.

- [ ] **Step 3: Write the Windows installation script**

Create `install.ps1`, equivalent in PowerShell. Requirements:

gx in` by default, respecting `$env:NGX_INSTALL_DIR`. - Accepts `$env:NGX_CHANNEL` and `$env:NGX_VERSION`, with the same meaning as the Unix version.This directory is writable without elevation, which is the right behavior for the platform — unlike Unix, on Windows there is no conventional `/usr/local/bin`.
- Detect architecture by `$env:PROCESSOR_ARCHITECTURE` (`AMD64` → `amd64`, `ARM64` → `arm64`).
- **Adds the directory to the user's `PATH`** if it is not already there, via `[Environment]::SetEnvironmentVariable('Path', ..., 'User')`, and warns that it is necessary to open a new terminal for the change to take effect. - Download `.zip` — not `.tar.gz` — and `checksums.txt`, check SHA256 with `Get-FileHash -Algorithm SHA256`, and only then extract with `Expand-Archive`.
Without this, the person installs and the command is not found.
- Installs in `$env:LOCALAPPDATA
- If the user points `NGX_INSTALL_DIR` to a path that requires elevation, such as `C:\Program Files`, it detects the lack of permission **before downloading** and instructs to run PowerShell as administrator — same rule as in Unix, without trying to elevate it alone.

The one-liner documented in the README:

```powershell
irm https://raw.githubusercontent.com/s0beran0/ngx/main/install.ps1 | iex
```

- [ ] **Step 4: Rotate**

Run: `sh install_test.sh`
Expected: all cases pass, and nothing was written outside the temporary directory.

`install.ps1` has no automated testing in this plan — it would require a Windows runner, and the Task D1 CI doesn't have one. Check it by hand on a Windows machine, or on a separate `windows-latest` runner, and record in the report what was tested and what wasn't. **Do not** report as verified what you were unable to run.

- [ ] **Step 5: Commit**

```bash
git add install.sh install.ps1 install_test.sh
git commit -m "feat: install scripts for unix and windows"
```

---

### Task D4: `ngx update` command

**Files:**
- Create: `internal/update/update.go`, `internal/update/github.go`, `internal/update/verify.go`, `internal/cli/update.go`
- Test: `internal/update/update_test.go`, `internal/update/verify_test.go`, `internal/cli/update_test.go`
- Modify: `internal/cli/root.go` — register the command

**Interfaces:**
- Consumes: `output.Version`, `output.PublicKey` (Task D2); `cli.Context`, `output.New`, the error constructors (Plan 1)
- Produces: `update.Release` (`Version`, `Prerelease bool`, `Assets []Asset`); `update.Channel` with `ChannelStable`/`ChannelBeta`; `update.Latest(ctx, channel) (*Release, error)`; `update.Verify(data, checksums, signature []byte, publickey, filename string) error`; `update.Apply(Binarypath string, new []byte) error`

- [ ] **Step 1: Investigate before writing**

This step is reading, not code. Before implementing, determine and note in the report:

1. **`aead.dev/minisign` v0.3.0** — the exact signature of `Verify`, how to get a `PublicKey` from the built-in string, and **whether the module has a non-stdlib dependency that requires cgo**. Read his `go.mod` from the module cache after `go get`. If it requires cgo, stop and report: the static build restriction is non-negotiable and the alternative would be to check Ed25519 directly with `crypto/ed25519`, decoding the minisign format by hand.
2. **Format of goreleaser's `checksums.txt`** — the order of the columns and the separator, so the parser does not depend on guesswork. Generate one with `goreleaser release --snapshot --clean` and read it.
3. **Running binary replacement** — on Linux and macOS, `rename(2)` on a running binary works because the old inode survives as long as there is an open descriptor, but writing *over* fails with `ETXTBSY`. Confirm the behavior and write the test that covers it.
4. **The same, on Windows** — there the running executable is frozen: renaming works, **deleting does not**. Confirm and determine whether Go's `os.Rename` is sufficient or whether you need `MoveFileEx` via `golang.org/x/sys/windows`. Useful reference: the strategy from `github.com/minio/selfupdate`, which renames the current binary to `.old`, puts the new one in place, and fails to delete the `.old` — leaving the cleanup for later. We don't need to adopt the library; we need to understand the technique.

Record the four responses in the report before moving on. Don't write code based on assumptions about any of them — in particular about 4, because the failure mode is an update that aborts in the middle and leaves the user without a working binary.

- [ ] **Step 2: Write the verification tests**

`internal/update/verify test.go` needs to cover, at a minimum:

- Valid signature and correct checksum: accepted.
- Valid signature but file checksum differs: refusal, with error citing the file name.
- Invalid signature for `checksums.txt`: refuses **without even looking at the checksum** — the order matters, because checking hash against an unauthenticated `checksums.txt` doesn't prove anything.
- Empty public key (local build, without `-ldflags`): refuses with an error explaining that this binary was not built to self-update. **Never** fall for "accept without checking".
- Missing file name from `checksums.txt`: refuse.

Generate a test key pair in the test itself and sign the test content instead of hard keying.

- [ ] **Step 3: Implement verification**

`internal/update/verify.go`, following the order: parse of the embedded public key → verification of the signature on the bytes of `checksums.txt` → parse of `checksums.txt` → comparison of the SHA256 of the artifact. Any failure aborts, and none of them have a bypass path.

- [ ] **Step 4: Implement the GitHub query**

`internal/update/github.go`. No new dependency: `net/http` and `encoding/json` are enough.

- Stable channel uses `/releases/latest`, which GitHub already filters to exclude pre-releases.
- Beta channel uses `/releases` and takes the first entry, which the API returns sorted by descending creation date.
- Respect the CLI global `--timeout` in `http.Client`.
- Treat 403 with `X-RateLimit-Remaining: 0` as a specific error, telling the user that the API limit has been reached — it is the most likely error in real use and a generic "failed" sends the person looking in the wrong place.

- [ ] **Step 5: Implement the replacement, with separate paths per system**

The behavior differs between Unix and Windows enough to justify two files with build tags: `internal/update/apply_unix.go` (`//go:build !windows`) and `internal/update/apply_windows.go` (`//go:build windows`), with the same signature `apply(path string, new []byte, perm os.FileMode) error`.

**Unix.** Write the new binary to a temporary file **in the same directory** as the current one — so the rename does not cross the filesystem —, apply the same permission as the original, `fsync`, and then `rename`. The old inode survives as long as the running process keeps it open.

**Windows.** The running executable cannot be deleted, but can be renamed. The sequence:

1. Write the new one as `ngx.exe.new` in the same directory.
2. Rename `ngx.exe` to `ngx.exe.old`.
3. Rename `ngx.exe.new` to `ngx.exe`.
4. **Tries** to remove `.old`, and ignores the failure — it is expected, because the file is still in use by the running process.

If step 3 fails after step 2 succeeds, restore: rename `.old` back. Leaving the user without `ngx.exe` is the worst possible outcome of this function, worse than not updating.

**Cleanup of `.old`.** Add a function `LimparResiduo(string path)` that removes a remaining `.old`, and call it **at `ngx`** initialization, silently — if it fails, it's not the user's problem. Without this, each update leaves an orphaned binary in the directory forever.

In both cases, if the directory lacks write permission, a clear error saying which directory and which privilege is needed, without trying to escalate.

Test both paths. The Windows one can be run on a separate `windows-latest` runner, or checked by hand — but record in the report what was actually run and what wasn't.

- [ ] **Step 6: Write the command**

`internal/cli/update.go`, with the flags:

- `--check`: only reports if there is a new version, without downloading anything. Exit 0 if updated, exit 7 if there is a pending update — reusing the "pending changes" code that the spec already defines.
- `--channel stable|beta`: default `stable`.
- `--version vX.Y.Z`: installs specific version, including older ones.

The output follows the usual envelope, with `data` bringing `current_version`, `latest_version`, `channel` and `updated`.

- [ ] **Step 7: Run the suite**

Run: `go test ./... -race`
Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add internal/update/ internal/cli/update.go internal/cli/root.go
git commit -m "feat(update): auto-atualizacao com verificacao de assinatura"
```

---

### Task D5: Installation and update README

**Files:**
- Modify: `README.md`

- Produces: installation, update and manual verification documentation**Interfaces:**
- Consumption: all above

- [ ] **Step 1: Write the sections**

Add to `README.md` sections covering:

**Installation on Linux and macOS** — the `curl` one-liner, showing **both** ways from the beginning, because most machines where `ngx` is useful require privilege to write to `/usr/local/bin`:

```sh
# installation on the system (needs privilege)
curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh | sudo sh

# installation of your user, without privileges
curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh | NGX_INSTALL_DIR=$HOME/.local/bin sh
```

Document `NGX_CHANNEL`, `NGX_VERSION` and `NGX_INSTALL_DIR`. Also show the manual download, for those who don't run scripts from the internet — and say that this is a legitimate preference, not paranoia.

**Installation on Windows** — PowerShell one-liner, default directory (`%LOCALAPPDATA%
gx in`), the warning about opening a new terminal for the `PATH` to be valid, and how to install it in a location that requires an administrator.

**Honest note about nginx on Windows** — `ngx` runs on Windows, but nginx there is officially **beta** according to nginx.org itself: it only uses `select()`/`poll()`, only one worker actually works, there is no support for UDP or QUIC, and it lacks the XSLT, image filter, GeoIP and built-in Perl modules. Say it bluntly and point to `https://nginx.org/en/docs/windows.html`.

Also explain that, on Windows, nginx is not normally installed via a package manager: it is located in an unpacked directory, like `C:
ginx-1.31.3\`, and uses the run directory as a prefix. So the auto-detection that works on Linux does not apply, and you need to point the path explicitly:

```powershell
ngx inspect -c C:\nginx-1.31.3\conf\nginx.conf
```

**Update** — `ngx update`, `ngx update --check`, `ngx update --channel beta`. Explain that the update checks the minisign signature and checksum before replacing the binary, and that a locally compiled binary refuses to self-update because it does not have an embedded public key.

On Windows, add that the executable in use cannot be removed by the system, so the old version remains as `ngx.exe.old` until the next run of `ngx`, which deletes it alone. If the person sees this file, it's expected — saying so avoids the support call.

On Linux and macOS, if `ngx` is installed in the system directory, the update needs the same privilege as the installation: document `sudo ngx update`.

**Channels** — that `v0.2.0` is stable and `v0.2.0-beta.1` is beta, that the beta channel receives both, and that the entire v0.x series is unstable in nature regardless of the channel.

**Manual check** — the exact commands to check a release by hand with `minisign -V` and `sha256sum -c`, with the project's public key in the text. Whoever audits needs this without having to read the updater code.

Be explicit about the warranty difference: the installation script checks **checksum**; `ngx update` checks **signature and checksum**.

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: instalacao, atualizacao e verificacao de releases"
```

---

## Coverage check

| Order | Task |
|---|---|
| CI via GitHub Actions | D1 |
| Release on main | D2 (by tag, triggered from `main`) |
| Installation via curl | D3 (`install.sh`) |
| Installation and update on Windows | D2, D3 (`install.ps1`), D4, D5 |
| Auto-update `ngx update` | D4 |
| Differentiated beta/stable releases | D2 (`prerelease: auto`) and D4 (`--channel`) |
| Documentation in README | D5 |
| `sudo` warning on installation | D3 (before download) and D5 |

## Execution order

D1 is independent and can go first. D2 needs the keys created outside the CI. D3 and D4 depend on D2 having published at least one release to test against something real — until then, they test against artifacts generated by `goreleaser --snapshot`. D5 is the closure.

## What this plan does not cover

Homebrew tap, `.deb`/`.rpm` packages, Docker image and publication in registries. All are direct additions to `.goreleaser.yaml` once the basic flow is working, and none change the architecture decided here.
