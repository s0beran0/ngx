# ngx — Remote Access Plan via SSH

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Administer nginx from a remote server without installing anything on it — `ngx` runs on your machine, reads the configuration and runs nginx over SSH.

**Architecture:** A transport layer with two implementations, local and SSH, behind the same interface. Remote parse reuses `ParseOptions.Open` that v0.1 already designed as injectable, plus a `Glob` that is now injected as well. No tree, selector, ID, hash, or wording logic changes: the remote is just another source of bytes and another place where commands run.

**Tech Stack:** `golang.org/x/crypto/ssh`, `github.com/pkg/sftp`, and — to be confirmed in Task R2 — a portable way to talk to `ssh-agent`.

## Terminology note

This project uses the word "agent" in two senses, and confusing them leads to
implement the wrong thing:

when there is no terminal. who authenticates. `ngx` is also used by humans directly, and is
  Nothing to do with AI.- **AI agent** — who *consumes* the output of `ngx`. That's why there is `--human`.
That's what the spec means
  - **`ssh-agent`** — an operating system program, prior to all this,
  in "the agent acts without reparsing" and is the reason the output is JSON by default
  which keeps unlocked SSH keys in memory and signs challenges on behalf of
  

is just an "agent", it is the consumer of the output.In this document, `ssh-agent` is always written like this, with the prefix. Where

**Spec:** `docs/superpowers/specs/2026-08-17-ngx-cli-design.md`. This plan fulfills part of the "multi-host via SSH" item that the spec puts in v1.0 (§16), anticipated by request.

**Prerequisite:** Plan 1 completed. This plan depends on `config.Parse`, `config.Combine`, `internal/runtime` and the output envelope.

## Global Constraints

- Go module: `github.com/s0beran0/ngx`. Go 1.25.
- **Zero CGO.** Every new dependency must be pure Go — check its `go.mod` before adding, and confirm with `CGO_ENABLED=0 go build` for all six platforms.
- **Works on Linux, macOS and Windows.** Nothing specific to a system without the equivalent path on the other two. This is particularly true for `ssh-agent`, which on Windows is not a Unix socket.
- No shell `exec`: remote commands are assembled with explicit and escaped arguments, never concatenated into a shell string.
- **Secret never goes in flag.** Password and passphrase come from prompt or environment variable. Flag appears in `ps`, shell history and CI logs.
- Code comments in Portuguese, without accents.
- **Commit messages never mention Claude or IA.** No `Co-Authored-By` trailer.

## Decisions

### DR1 — Strict host key checking, with explicit escaping

`ngx` uses the user's `known_hosts` and **refuses** an unknown host or host whose key has changed, like `ssh` does. Anyone who needs to bypass passes `--insecure-host-key`, which is verbose on purpose and is visible in the command.

*Why:* remote `ngx` transmits the server configuration and executes privileged commands on it. A client that accepts any host key allows any machine on the route to impersonate the server, capturing credentials and receiving commands. Accepting unknown keys silently is the behavior that makes SSH encryption useless.

*Accepted cost:* the first access to a new host requires that it be in `known_hosts` — which is resolved with a manual `ssh` first, and is the same friction that `ssh` already imposes.

### DR2 — `ssh-agent` first, `~/.ssh/config` second, flags on top

The order of resolution is: explicit flags win; whatever is missing comes from `~/.ssh/config` for that host; authentication tries `ssh-agent` before any key files.

*Fixes measured against a real production nginx* (Oracle Linux 9, VPN), all cases where `ngx` refused connection that `ssh` made:

1. **A single public key method.** Offering `ssh-agent` and file as separate methods fails: as soon as the first one runs out without authenticating, the next one doesn't save. OpenSSH offers everything in one method. Without this, anyone with keys on the agent that did not fit that host would not be able to connect.
2. **Search for default keys from `~/.ssh`.** `ssh` tries `id_rsa`, `id_ecdsa` and `id_ed25519` on its own — this is what makes `ssh web1` work without configuration. Without this, the promise of this decision (`ngx --host web1` works for those who already have `ssh web1`) was false: on the measured host the key that authenticated was `~/.ssh/id_rsa`, outside of `~/.ssh/config` and outside of the agent.
3. **Key named in `--key` comes first** (below).

*Exception measured against real server:* when the user names the key in `--key`, it is offered **before** `ssh-agent`. sshd's default `MaxAuthTries` is 6; each key loaded into `ssh-agent` takes one attempt, and a developer often has several. With the agent in front, the explicitly requested key was never offered and the connection died with `no supported methods remain` — a message that does not point to the cause. It's the same problem that `IdentitiesOnly=yes` solves in `ssh`. Without `--key`, the original order holds.

*Why:* With `ssh-agent`, the private key is never read by `ngx` — it sends the challenge and receives the signature. Less code of ours touching key material is less surface to make mistakes on. And reading `~/.ssh/config` means that `ngx --host web1 inspect` works for those who already have `ssh web1` working, without reconfiguring anything.

### DR3 — Nothing is installed on the remote server

`ngx` does not copy binary to the destination, not even temporarily. It reads files over SFTP and runs the `nginx` that already exists there.

*Why:* is the requirement. Writing executables in `/tmp` on a production server is the kind of thing that triggers EDR alerts and that an operator is right not to want.

*Accepted cost:* plus network trips. One SFTP read per file of the effective configuration.

*Measured on real production nginx* (Oracle Linux 9, nginx 1.20.1, VPN access): effective configuration has **132 files** and 9,822 lines. The original estimate for this plan — "thirty 'includes'" — was wrong more than four times. With 132 sequential trips, VPN latency dominates response time and an interactive read stops being interactive.

*Consequence:* parallelizing the readings goes from being a "fix if it becomes a problem" to becoming a design requirement for R4. Serialize only what the dependency requires: `include` is only known after reading who declares it, so the parallelism is per tree level, not over the entire list.

### DR7 — unreadable `~/.ssh/config` degrades with warning, never aborts

The parse library honors `Host` (with wildcard and negation), `Include`, `Match all` and `Match Host`. Any other criteria — `user`, `final`, `canonical`, `exec` — makes the parse of the **whole file** fail, not just that entry. Details and sources in `docs/superpowers/specs/2026-08-17-ngx-remoto-dependencias.md`.

A `~/.ssh/config` with `Match user deploy` is perfectly valid for `ssh` and not at all rare. If `ngx` aborted, it would break for anyone who has a legitimate file, due to our limitation.

So: parse failure becomes a **`warning`** severity diagnosis in the envelope, saying which file and which line `ngx` did not understand, and the resolution follows what came from flags and defaults. What **cannot** happen is that `ngx` ignores the file silently and connects to a host other than the intended one — the warning is what prevents this from becoming a surprise.

### DR8 — Privileged reading is minimal, in three steps

On a real server, the configuration is rarely completely readable by the connection user: most files are public and a handful hold credentials and are restricted to root. Measured on production nginx: **one** file out of 132.

The **no** answer is to advise those who use it to loosen permissions on the server. That file is restricted on purpose, and a tool that requires `chmod` in `/etc/nginx` to work trades host security for reading convenience — replicated across the entire fleet by those who just want `ngx` running.

With `--sudo`, reading goes down three steps, stopping at the first one that responds:

1. **SFTP as the connection user.** No privileges whatsoever. Covers most.
2. **`sudo -n cat <file>`**, only for the rejected file. The other 131 continue to be read without privilege.
3. **`sudo -n nginx -T`**, when not even `cat` passes. This is the case of the hardened server, whose `sudoers` releases specific commands — typically nginx itself — and refuses a generic `cat`. Without this step, privileged reading would be useless precisely where `sudo` is well configured.

The dump is the last step, and not the first, because `nginx -T` requires **valid** configuration: the time when you most need to read the configuration is when it broke, and there only reading file by file responds.

Without `--sudo` none of this happens — DR5 is still valid —, but the diagnosis starts to say that `--sudo` solves it, so that the dead end doesn't push the operator towards the wrong solution.

*Transparency:* every path that required privilege appears on the envelope, with the origin. Reading a server's configuration with `sudo` cannot happen silently.

*What `--sudo` DOES NOT do:* restrict by path list. It was considered and discarded. Non-standard installation would break — the measured server itself includes from `/etc/letsencrypt`, outside of `/etc/nginx` — and would protect little, because the real vector is within the legitimate configuration tree: verified that a file that is not nginx syntax (a `/etc/shadow`, for example) fails to parse and has **no** emitted content; what appears in the output is a file that is already a valid configuration.

Instead of restricting, `ngx` marks what is anomalous: privileged reading of a path **outside any directory that the configuration already reached without privilege** comes out as `warning`, not `info`. The trust tree is derived from the configuration itself — the directories it actually references — and not from a fixed list, so installation in `/opt` works the same. The top file is never an anomaly: it was the operator who named it.

*Proportion, said honestly:* whoever can write to the target's `nginx.conf` can now exfiltrate with `proxy_pass` and already run master as root. `ngx` changes the **destination** of the data, not the access to it. This is observability, not containment.

### DR6 — `ngx` does not use `sftp.Client.Glob`

`Glob` from `github.com/pkg/sftp` **discards I/O errors by contract**. The function's own comment says: *"Glob ignores file system errors such as I/O errors reading directories. The only possible returned error is ErrBadPattern"* (`match.go:40-42`). And in the path without metacharacter it is literal: `file, err := c.Lstat(pattern); if err != nil { return nil, nil }` — dropped connection returns no results and no errors.

`ngx` implements its own remote glob over `ReadDir` + `path.Match`, propagating I/O error as an error.

*Why:* `include /etc/nginx/conf.d/*.conf` on an unstable link would return zero files silently, and `ngx` would present the server configuration without the 112 files it has — as if the server genuinely didn't have them. A tool read by an AI agent cannot be reliably incomplete: the consumer cannot be suspicious.

*Note:* stdlib's `filepath.Glob` has the same semantics, and locally this almost never matters. It's the same premise that SSH reverses — read failure stops being rare and becomes routine. It also applies to the parked item from Task 7 in the ledger, for the same reason.

### DR5 — Privilege is explicit, never inferred

Measured on real production server: `nginx -T` **fails** for regular user (`opc`) and only works via `sudo`. This is no exception — nginx configuration is usually readable only by root, and on a production host `sudo` is often enabled without a password. In other words: the path that “just works” is to silently escalate privilege.

`ngx` **doesn't** do this. If a remote command needs privilege, it only runs with explicit `--sudo` in the command; without the flag, `ngx` reports that the command requires privilege and what it is — doesn't try again with `sudo`, doesn't guess.

*Why:* a tool made to be driven by an AI agent that escalates privileges alone, on a production server, turns a read error into a `root` command. The friction of typing `--sudo` is the record that someone decided. And since `ngx` already has a structured envelope, "needs privilege" is an actionable diagnosis, not a dead end.

*Consequence for R4:* state detection needs to distinguish "couldn't read" from "doesn't exist", and never degrade into silence. Unavailable field is omitted — the spec rule already covers this.

### DR4 — The crossplane `Glob` is now injected

Today `ngx` injects `Open` but not `Glob`, so crossplane resolves `include conf.d/*.conf` with `filepath.Glob` on the **local** disk. This is registered as a known limitation of v0.1 and here it becomes a defect: pointed to a remote host, `ngx` would list files from the operator's machine and treat them as server configuration.

*Consequence:* Task R3 is mandatory and blocks the others. Without it, the remote lies.

---

### Task R1: Transport layer

- Test: `internal/transport/local_test.go`**Files:**
- Create: `internal/transport/transport.go`, `internal/transport/local.go`

**Interfaces:**
- Consumptions: no previous tasks
- Produces: the `transport.Transport` interface with `Open(path string) (io.ReadCloser, error)`, `Glob(pattern string) ([]string, error)`, `Run(ctx context.Context, argv []string) (stdout, stderr []byte, exitCode int, err error)`, `Close() error`, and `Describe() string`; the `transport.Local()` implementation

- [ ] **Step 1: Write the test**

`internal/transport/local_test.go` covers: `Open` of existing and non-existent file; `Glob` marrying and not marrying; `Run` from a command that exits with zero and another that exits with a different code, verifying that `exitCode` is reported **and** that `err` is nil in this case — non-zero exit code is a result, not a transport error. A transport error is the binary does not exist or the connection drops.

This distinction is the central point of the test: confusing the two makes an `nginx -t` that fails the configuration look like an infrastructure failure.

- [ ] **Step 2: Define the interface and implement the location**

`Local()` is a thin wrapper over `os.Open`, `filepath.Glob` and `exec.CommandContext`. `Describe()` returns something like `"local"`, to appear in the `meta` of the envelope and whoever consumes the output will know what they operated against.

- [ ] **Step 3: Run and commit**

Run: `go test ./internal/transport/ -race`

```bash
git add internal/transport/
git commit -m "feat(transport): interface de transporte e implementacao local"
```

---

### Task R2: Portable SSH Client

- Test: `internal/transport/ssh_test.go`, `internal/transport/sshconfig_test.go`**Files:**
- Create: `internal/transport/ssh.go`, `internal/transport/agent_unix.go`, `internal/transport/agent_windows.go`, `internal/transport/sshconfig.go`

**Interfaces:**
- Consumptions: `transport.Transport` (R1)
- Produces: `transport.SSHOptions` (`Host`, `Port`, `User`, `KeyPath`, `Password`, `KnownHostsPath`, `InsecureHostKey`, `Timeout`); `transport.SSH(opts SSHOptions) (Transport, error)`; `transport.ResolverSSHConfig(host string) (SSHOptions, error)`

- [ ] **Step 1: Investigate before writing a line**

This step is reading and experimenting, not code. Determine and **record in the report** each answer, with the source:

1. **`ssh-agent` on Windows.** What is the exact named pipe path used by OpenSSH on Windows? How to connect to it in Go? Evaluate `github.com/Microsoft/go-winio` — read its `go.mod` and confirm that it is pure Go on `x/sys/windows`, without cgo. If there is an alternative without new dependencies, choose it. **Don't assume the pipe path**: confirm it in the OpenSSH-Portable documentation or in the source.
2. **`golang.org/x/crypto/ssh/agent`** — the function that creates the client from a connection, and how to transform the client into `ssh.AuthMethod`.
3. **`golang.org/x/crypto/ssh/knownhosts`** — how to construct `HostKeyCallback`, and **what error exactly** does it return when the host is unknown versus when the key has changed. The two cases need different messages: unknown key is normal friction, changed key is possible attack and the message has to say that.
4. **`github.com/pkg/sftp`** — is it pure Go? How to open a file for reading and how to glob (does it have `Glob`?).
5. **Parser from `~/.ssh/config`** — is there a mature pure Go library, like `github.com/kevinburke/ssh_config`? Does it resolve `Include`, `Match` and wildcards in `Host`? If support is partial, decide and document which subset `ngx` honors — better to honor little and say, than to honor badly in silence.
6. **`~/.ssh`** path on all three platforms: confirm that `os.UserHomeDir()` resolves correctly on Windows.

If any item requires code, stop and report before proceeding.

- [ ] **Step 2: Write the configuration and host key resolution tests**

`sshconfig_test.go` uses files from `~/.ssh/config` in `t.TempDir()` and checks: `HostName`, `User`, `Port` and `IdentityFile` being read; wildcard in `Host` matching; explicit flag overwriting the file; host missing from the file returning the defaults without error.

`ssh_test.go` checks the **no network** host key policy, calling the callback directly: host present in `known_hosts` with the right key passes; missing host fails with error mentioning the host and how to add it; host present with **different** key fails with error that explicitly says that the server key has changed and that this could be an attack; and `InsecureHostKey` passing any key but recording a `warning` severity diagnostic on the envelope — escaping cannot be silent.

- [ ] **Step 3: Implement**

Authentication order: `ssh-agent`, then key in file (with passphrase prompt if necessary), then password. Password comes from `NGX_SSH_PASSWORD` or from a terminal prompt — **never** from flag; if someone adds a password flag, the review should fail.

The `agent_unix.go` and `agent_windows.go` files carry build tags and expose the same connection function to `ssh-agent`. When there is no `ssh-agent` available, this is not an error: just that authentication method is not included in the list.

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/transport/ -race`, and `CGO_ENABLED=0 go build` for all six platforms.

```bash
git add internal/transport/
git commit -m "feat(transport): cliente ssh com known_hosts estrito e ssh-agent portavel"
```

---

### Task R3: Parse with `Glob` injected

- Test: `internal/config/parse_test.go`**Files:**
- Modify: `internal/config/parse.go` — pass `Glob` to crossplane

- Produces: `ParseOptions.Glob func(pattern string) ([]string, error)`, injected into `crossplane.ParseOptions`**Interfaces:**
- Consumes: `config.ParseOptions` (Plan 1, Task 7)

- [ ] **Step 1: Confirm subscription on the crossplane**

Read `crossplane.ParseOptions` in the module cache and confirm the name and exact signature of the `Glob` field. Don't write from memory.

- [ ] **Step 2: Write the test that fails**

A test with an in-memory filesystem containing `nginx.conf` with `include conf.d/*.conf` and two files matching the pattern, **and** a file with the same name on the real disk that should not be read. Without the correction, the crossplane lists the real disk; with it, it only lists the injected filesystem.

This test is the reason this task exists: today, pointed to a remote host, `ngx` would read `conf.d/*.conf` from the operator's machine.

- [ ] **Step 3: Implement and run**

Please note: `parse.go` has undergone three rounds of patching and has delicate concurrency logic and font caching. The change here is to add a field and pass it through. **Don't** touch `MirroredReading`, `Sourcecache`, `CollectErrors` or error handling.

Run: `go test ./internal/config/ -race`

- [ ] **Step 4: Commit**

```bash
git add internal/config/
git commit -m "fix(config): injeta Glob no crossplane para nao listar disco local"
```

---

### Task R4: Remote Runtime

- Test: existing runtime tests, plus cases with false transport**Files:**
- Modify: `internal/runtime/` — receive a `Transport` instead of calling `exec` directly

- Produces: runtime functions now accept a `Transport`**Interfaces:**
- Consumptions: `transport.Transport` (R1)

- [ ] **Step 1: Write the tests**

Use um `Transport` falso que devolve saídas gravadas, e verifique que a detecção do nginx, o `nginx -t` estruturado e o `nginx -T` funcionam idênticos com transporte local e remoto. The point is that the output parser doesn't know where the bytes came from.

Also cover: `nginx` not found on remote host, and command coming out non-zero.

- [ ] **Step 2: Refactor the runtime**

Replace direct calls to `exec.Command` with `Transport.Run`. Preserve the distinction of Task R1: non-zero exit code is a result, not an error.

About process state: pidfile reading works over SFTP, but worker count and master start time depend on process inspection, which differs between systems and is more fragile over SSH. Keep the spec rule — **unavailable field is omitted, never estimated**.

- [ ] **Step 3: Run and commit**

Run: `go test ./... -race`

```bash
git add internal/runtime/
git commit -m "refactor(runtime): executa via transporte, local ou remoto"
```

---

### Task R5: Global flags and CLI integration

- Test: `internal/cli/root_test.go`**Files:**
- Modify: `internal/cli/root.go` — connection and transport construction flags

- Produces: `cli.Context.Transport`**Interfaces:**
- Consumptions: `transport.SSH`, `transport.Local`, `transport.ResolverSSHConfig` (R1, R2)

- [ ] **Step 1: Write the tests**

- Without `--host`, transport is local and no SSH is built.
- With `--host`, the values from `~/.ssh/config` are applied and the flags are overwritten.
- A password flag **does not exist**; if someone tries `--password`, cobra returns an unknown flag error.
- `--insecure-host-key` produces a `warning` diagnostic on the envelope.
- The `meta` of the envelope carries which host the operation ran against.

- [ ] **Step 2: Add the flags**

`--host`, `--port`, `--user`, `--key`, `--insecure-host-key`, `--known-hosts`. The global `--timeout` already exists and takes effect for the connection.

Every v0.1 reading command (`status`, `inspect`, `get`, `tree`, `test`, `diff`) works remotely without changing itself, because it receives transport via the context. `fmt --write` writes via SFTP.

- [ ] **Step 3: Run and commit**

Run: `go test ./... -race`

```bash
git add internal/cli/
git commit -m "feat(cli): flags de conexao remota e transporte no contexto"
```

---

### Task R6: Actual integration and documentation

- Modify: `README.md`**Files:**
- Create: `internal/transport/integration_test.go` (`//go:build integration`), `docs/remoto.md`

- Produces: integration suite against real SSH, and documentation**Interfaces:**
- Consumption: all above

- [ ] **Step 1: Write the integration test**

Under build tag `integration`, upload a container with `sshd` and `nginx`, with a test key generated in the test itself, and check end-to-end: remote `inspect` returning the container tree; `include` with glob resolving the **container** files; remote `test` reporting syntax error with file and line; and rejection by unknown host key before the host is added to `known_hosts`.

The glob case is the most important: it is the defect that Task R3 fixed, and this is the test that proves that it does not return.

**The bench has to reproduce the measured form in production, not an easy case.** A container with a ten-line `nginx.conf` passes everything and proves nothing. Measured on a real production nginx (Oracle Linux 9, nginx 1.20.1), the bench needs:

It is the number that makes the sequential latency visible and that justifies the parallelism per level required in R4. A test that measures time with 130 files is what prevents performance regression.
- **Three wildcard patterns**, not one: `conf.d/*.conf`, `default.d/*.conf` and `modules/*.conf`. - **`nginx -T` readable only by root**, with `sudo` released without password for the test user — exactly the DR5 trap. And, for the glob test to be valid, a homonymous file on the **local** disk that would only appear if `Glob` was not injected.
The test has to prove that without `--sudo` `ngx` reports the privilege requirement, and that it **doesn't** scale on its own.
- **Order of 130 files** in effective configuration, not three. - **A secret within the configuration** (private key, `auth_basic_user_file`) to exercise end-to-end redaction over the remote path.

The bench is an artifact of the repository, versioned, targeting the `Makefile`. Whoever clones the project must be able to run the integration with a command. And integration tests are behind the build tag: `go test ./...` without the tag remains green on a machine without Docker.

- [ ] **Step 2: Document**

In `docs/remoto.md` and in a section of `README.md`:

- The minimum use, `ngx --host web1.exemplo.com inspect`, explaining that it works without flags for those who already have `ssh web1.exemplo.com` working.
- That **nothing is installed on the server** — `ngx` reads via SFTP and runs nginx that is already there.
- The authentication order, and which password comes from `NGX_SSH_PASSWORD` or from the prompt, never from the flag, explaining why: flag leaks in `ps`, in the history and in the CI log.
- What unknown host is refused, how to add it to `known_hosts`, and what `--insecure-host-key` means — including that it should not become a habit.
- On Windows, the `ssh-agent` service is disabled and needs to be enabled, using the commands.
- The latency caveat: each `include` is a network read.

- [ ] **Step 3: Commit**

```bash
git add internal/transport/integration_test.go docs/remoto.md README.md
git commit -m "test(transport): integracao ssh real; docs de operacao remota"
```

---

## Coverage check

| Pedido | Task |
|---|---|
| Executar em servidor remoto via SSH | R1, R2, R4, R5 |
| Passar host, user, porta | R2 (`~/.ssh/config`), R5 (flags) |
| Senha, quando o servidor exigir | R2 (env ou prompt, nunca flag) |
| Chave SSH e caminho da chave | R2 (`--key`, `IdentityFile`, `ssh-agent` antes) |
| Não instalar o CLI na VM | DR3, R1 (SFTP mais exec remoto) |
| Funcionar em Linux, macOS e Windows | Global Constraints; R2 Step 1 item 1 |

## Known limitation, to be resolved later

The remote connection is opened in `PersistentPreRunE`, before the command runs. This means that `ngx --host web1 version` opens an SSH session to print a string that is purely local: slow, capable of failing for reasons unrelated to the command, and surprising to anyone reading the output. The same will be true for `ngx update`.

*Why it wasn't corrected together:* the correction is a command note marking who doesn't touch the target. I tried, and it breaks six `internal/cli` tests that precisely use `version` as a vehicle to exercise the SSH path — they count connections and would start counting zero. Fixing them requires mounting configuration fixtures on each one, because `inspect` needs a `.conf`. Trading a minor, read-only defect for a newly written suite rewrite at the end of a long session is the wrong trade.

*How to solve it, when the time comes:* give the test context a fake command that declares it needs the transport, and move the six tests to it. Then the annotation enters without touching any fixture, and `version` and `update` stop connecting.

## What this plan does not cover

Multi-host in one call (`--hosts a,b,c` with parallel execution), bastion and jump (`ProxyJump`), tunnel, and remote transactional writing — the latter depends on the v0.2 mutation plan. They are natural extensions of the same transport layer, and none of them change the decisions made here.
