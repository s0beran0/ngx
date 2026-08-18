# SSH remote access dependencies

This document records the investigation required by Step 1 of Task R2 of the plan
`docs/superpowers/plans/2026-08-17-ngx-remoto-ssh.md`. It exists because three
defects in this base were born from integration code written from memory: here
every statement has a source, and the source is the library code or the code of the
OpenSSH, not the memory of who wrote it.

Versions investigated (the ones that `go.mod` should fix):

| Module | Version | Paper |
| --- | --- | --- |
| `golang.org/x/crypto` | v0.55.0 | `ssh`, `ssh/agent`, `ssh/knownhosts` |
| `github.com/pkg/sftp` | v1.13.11 | remote read and glob |
| `github.com/kevinburke/ssh_config` | v1.6.0 | parse `~/.ssh/config` |
| `github.com/Microsoft/go-winio` | v0.6.2 | named pipe of `ssh-agent` on Windows |

Terminology reminder: `ssh-agent` is the operating system program that
Keep unlocked keys. It has no relation to the *AI agent* that consumes the
`ngx` output.

## Proof of absence of cgo

Reading the `go.mod` of a dependency is **not** proof: `go.mod` says nothing about
`import "C"` files hidden behind build tags. The proof is to compile.

A disposable module importing all four libraries — including one file
with build tag `windows` that calls `winio.DialPipe` — was compiled with:

```
CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build ./...
```

Result, with Go 1.25.9:

| Platform | Result |
| --- | --- |
| linux/amd64 | OK |
| linux/arm64 | OK |
| darwin/amd64 | OK |
| darwin/arm64 | OK |
| windows/amd64 | OK |
| windows/arm64 | OK |

Non-stdlib dependency graph of `windows/amd64` build: `x/crypto/ssh`
(+ `blowfish`, `chacha20`, `cryptobyte`, `curve25519`, `poly1305`,
`bcrypt_pbkdf`), `pkg/sftp` (+ `kr/fs`), `kevinburke/ssh_config`,
`Microsoft/go-winio` (+ `internal/fs`, `internal/socket`, `internal/stringbuffer`,
`pkg/guid`) and `golang.org/x/sys/windows`. Nothing more.

About `go-winio`: its `go.mod` declares `github.com/sirupsen/logrus`,
`golang.org/x/tools` and `golang.org/x/mod`. None of the three make it into the build —
These are test and tool dependencies. The proof module's `go mod tidy`
closed with just two `indirect`: `github.com/kr/fs` and `golang.org/x/sys`.
`x/sys` already uses `ngx`.

**Conclusion: none of the candidates require cgo.** The static binary continues
possible on all six platforms.

## 1. `ssh-agent` on Windows

The Windows `ssh-agent` is not a Unix socket: it is a **named pipe**. The path is

```
\\.\pipe\openssh-ssh-agent
```

Source, server side — `ssh-agent.exe` itself creates the pipe with that name
literally:

- `PowerShell/openssh-portable`, branch `latestw_all`,
  `contrib/win32/win32compat/ssh-agent/agent.c:50`:
  `#define AGENT_PIPE_ID L"\\.\pipe\openssh-ssh-agent"`, used in
  `CreateNamedPipeW` from line 119-120.

Source, client side — Windows' `ssh.exe` **doesn't** have the path built into the
authentication code; it fills `SSH_AUTH_SOCK` with this value in `wmain`
when the variable is empty, and from then on the portable code uses the
normally variable:

- `contrib/win32/win32compat/wmain_common.c:53-54`:
  ```c
  if (getenv("SSH_AUTH_SOCK") == NULL)
          _putenv("SSH_AUTH_SOCK=\\.\pipe\openssh-ssh-agent");
  ```
- `authfd.c:122-134` (`ssh_get_authentication_socket`) reads
  `SSH_AUTHSOCKET_ENV_NAME`, defined as `"SSH_AUTH_SOCK"` in `ssh.h:58`, and
  returns `SSH_ERR_AGENT_NOT_PRESENT` if empty.
- On Windows, `connect(AF_UNIX)` from `authfd.c` falls under the compatibility layer
  `contrib/win32/win32compat/fileio.c:122-153` (`fileio_connect`), which opens the
  path with `CreateFileW(..., OPEN_EXISTING, FILE_FLAG_OVERLAPPED | ...)` and
  retry while the error is `ERROR_PIPE_BUSY`.

**Rule that `ngx` adopts**, mirroring OpenSSH: honor `SSH_AUTH_SOCK` if
is set — on any platform — and only on Windows and only when it
is empty, falls into the pattern `\.\pipe\openssh-ssh-agent`. This makes `ngx`
work with native `ssh-agent` without configuration while respecting
who points `SSH_AUTH_SOCK` to another agent (1Password, gpg-agent, a relay
of WSL).

### How to connect to Go

Two routes, both proven to be costless:

there is also `DialPipeContext` (`pipe.go:255`). The `net.Conn`
**A — `github.com/Microsoft/go-winio` (recommended).**
returned satisfies `io.ReadWriter`, which is what `agent.NewClient` asks for. `func DialPipe(path string, timeout *time.Duration) (net.Conn, error)`
A
(`pipe.go:237`); library handles `ERROR_PIPE_BUSY` with wait, like OpenSSH does.

**B — `os.OpenFile` from the standard library.** Opening the pipe as a file is
literally what OpenSSH's `fileio_connect` does, and `*os.File` is also a
`io.ReadWriter`. Zero new dependency.

`x/sys` is already in the project) and it covers the retry of
third parties is the right decision precisely where we are unable to test.`ERROR_PIPE_BUSY` and the overlapped I/O semantics that route B would leave for
Choose: **A**. we reimplement blindly — neither is testable on this basis, which has no
The actual dependency cost is low (no new modules other than the
Windows in the development loop. `go-winio` itself; Prefer the path already taken by

Absence of `ssh-agent` **is not an error**: it is just an authentication method that does not
enters the list.

## 2. `golang.org/x/crypto/ssh/agent`

- `func NewClient(rw io.ReadWriter) ExtendedAgent` — `ssh/agent/client.go:351`.
  If `rw` is also `io.Closer`, the client uses pipelining; otherwise
  serializes the calls. A `net.Conn` (Unix or named pipe) satisfies both.
- The client turns `ssh.AuthMethod` through signers:
  `func (c *client) Signers() ([]ssh.Signer, error)` — `ssh/agent/client.go:819` —
  combined with `func PublicKeysCallback(getSigners func() ([]Signer, error)) AuthMethod`
  — `ssh/client_auth.go:492`.

That is: `ssh.PublicKeysCallback(agent.NewClient(conn).Signers)`. Use
`PublicKeysCallback` instead of `ssh.PublicKeys(signers...)` (`client_auth.go:486`)
matters: the list of keys is fetched at authentication time, so a key
added to `ssh-agent` after `ngx` started is still seen.

The other authentication order methods:
`ssh.ParsePrivateKey` (`ssh/keys.go:1314`),
`ssh.ParsePrivateKeyWithPassphrase` (`ssh/keys.go:1326`),
`ssh.Password` (`ssh/client_auth.go:228`) and
`ssh.PasswordCallback` (`ssh/client_auth.go:234`).

## 3. `golang.org/x/crypto/ssh/knownhosts` — and the difference between the two errors

construction**, not in the callback. leak a raw `PathError`.Checked: the error is a `*fs.PathError`
`func New(files ...string) (ssh.HostKeyCallback, error)` — `knownhosts.go:417`.
(`open ...: no such file or directory`). It opens each file with `os.Open`; `ngx` needs to handle "user never
**non-existent file returns error
used ssh on this machine" as its own case, with its own message, and not

The distinction that Task R2 requires is in a single type, broken down by a
field — not by two different types:

```go
// knownhosts.go:317-330
type KeyError struct {
        // Want holds the accepted host keys. For each key algorithm,
        // there can be multiple hostkeys.  If Want is empty, the host
        // is unknown. If Want is non-empty, there was a mismatch, which
        // can signify a MITM attack.
        Want []KnownKey
}

func (u *KeyError) Error() string {
        if len(u.Want) == 0 {
                return "knownhosts: key is unknown"
        }
        return "knownhosts: key mismatch"
}
```

Therefore:

| Situation | How to detect | Message from `ngx` |
| --- | --- | --- |
| Unknown host | `errors.As(err, &ke)` e `len(ke.Want) == 0` | normal friction: say the host and line to add |
| **Key changed** | `errors.As(err, &ke)` e `len(ke.Want) > 0` | **possible attack**: say this in all letters, and show `ke.Want[i]` |
| Key revoked | `errors.As(err, &re)` com `*knownhosts.RevokedError` (`knownhosts.go:333-339`) | refuses, informing the file and line |

`**KeyError` — that is, a `var ke *knownhosts.KeyError` variable.The error is always returned by pointer (`&KeyError{}` in `knownhosts.go:375` and
`&RevokedError{...}` in `knownhosts.go:345`), then `errors.As` needs

`ssh.ParseAuthorizedKey`. `"knownhosts: key is unknown"` with empty `Want` for missing host.This is exactly how the three cases above were
How to test, without networking: set up `known_hosts` in `t.TempDir()`, call
confirmed; `knownhosts.New`, and invoke the returned `ssh.HostKeyCallback` directly,
the observed output was `"knownhosts: key mismatch"` with
passing a `*net.TCPAddr` as `remote` and a `ssh.PublicKey` obtained from
`Want[0] = "<file>:1: ssh-ed25519 AAAA..."` for exchanged key, and

`func Line(addresses []string, key ssh.PublicKey) string` (`knownhosts.go:461`).
`Line([]string{Normalize("10.0.0.9:22")}, key)` produces
For the unknown host message, `knownhosts` provides both pieces of the
`10.0.0.9 ssh-ed25519 AAAA...` — port 22 is omitted, non-standard ports seen
line that the user needs to paste:
`[host]:port`.`func Normalize(address string) string` (`knownhosts.go:441`) and

## 4. `github.com/pkg/sftp`

Pure Go (confirmed by the cross-build from the table above; `go.mod` only declares
`kr/fs`, `x/crypto`, `x/sys` and the test testify).

Opens with `O_RDONLY`; The syntax is that of `path.Match`, the same as `filepath.Glob`,
  `*File` implements `io.Reader`, which is what
  and therefore the same as what the crossplane already expects from the `Glob` injected into Task R3.- Client: `func NewClient(conn *ssh.Client, opts ...ClientOption) (*Client, error)`
  `ParseOptions.Open` from v0.1 needs it.
— `client.go:197`.
- **Yes, it has a glob**: `func (c *Client) Glob(pattern string) ([]string, error)`
  - Reading: `func (c *Client) Open(path string) (*File, error)` — `client.go:657`.
  — `match.go:43`. 

Two caveats from the source that need to become conscious behavior:

- `Glob` **ignores file system errors**, including I/O when reading
  directories (`match.go:40-42`). The only possible error is `ErrBadPattern`. One
  directory without read permission on the server silently produces a
  Shorter list — not an error. If a remote `include` matches zero
  files, `ngx` cannot distinguish "does not exist" from "could not read".
- Pattern without metacharacter becomes an `Lstat`, and a `Lstat` that fails returns
  `nil, nil` (`match.go:44-48`) — again, silent absence.

## 5. `~/.ssh/config` parser

`github.com/kevinburke/ssh_config` v1.6.0 is pure Go and **has no
dependency** (his `go.mod` only has the `module` and `go 1.18` lines).

The project's README states that "the `Match` directive is currently unsupported".
**README is out of date**: v1.6.0 source implements a subset of
`Match`. What counts is `parser.go`, empirically verified.

### What is honorable

| Feature | Situation | Source |
| --- | --- | --- |
| `Host` com wildcard (`web*`, `?`) | Yes | `config.go:491` (`NewPattern`), `config.go:550` (`Matches`) |
| `Host` com negação (`!web9`) | yes — matching a negated pattern discards the entire block | `config.go:550-568` |
| `Include`, inclusive com wildcard no caminho | yes, up to 5 levels of recursion (`ErrDepthExceeded`) | `config.go:705-795` |
| `Match all` | yes, treated as `Host*` | `parser.go:184-195` |
| `Match Host <padrões>` | Yes | `parser.go:196-222` |
| `IdentityFile` múltiplo | yes, via `GetAll` | `config.go:406` |
| Case-insensitive keys | Yes | `config.go:376` |

### What Isn't Honorable — and How It Fails

`Match exec` is **rejected on purpose**, with a comment in the source saying
why: running arbitrary command would leave an untrusted `~/.ssh/config`
run code on the machine of the person doing the parsing (`parser.go:224-230`). This is
aligned with `ngx` policy of not doing shell `exec`; the decision of the
library is our decision.

Any other `Match` criteria — `user`, `localuser`, `final`, `canonical`,
`originalhost`, `exec` — produces **entire file parse error**, not a
directive ignored. Observed messages:

```
(1, 7): ssh_config: Match Exec is not supported
(1, 7): ssh_config: unsupported Match criterion "user"
(1, 7): ssh_config: unsupported Match criterion "final"
(1, 7): ssh_config: unsupported Match criterion "canonical"
(1, 7): ssh_config: unsupported Match criterion "originalhost"
(1, 7): ssh_config: unsupported Match criterion "localuser"
```

reading the entire file, not just that block.**This is the most important practical consequence of this investigation: **a user
with `Match final` or `Match exec` in `~/.ssh/config` makes `ngx` fail to

### Decision

`IdentityFile`. Nothing else about `ssh_config` influences the behavior — in
`ngx` honors: `Host` (with wildcard and negation), `Include`, `Match all` and
particular `ProxyJump`, `ProxyCommand`, `ControlMaster` and `IdentityAgent`
`MatchHost`. They are **not** honored, and they are not honored in silence: those who depend on them need
The directives read are `HostName`, `User`, `Port` and
know that `ngx` ignores them.

supported, that resolution by `~/.ssh/config` was skipped, and that the flags
explicit (`--host`, `--user`, `--port`, `--key`) still work. And when parse fails due to an unsupported `Match`, `ngx` **doesn't** handle it
Fail
as "missing file". with "I couldn't find the host" when in fact we can't read the file it would be
It issues a diagnosis saying which criterion is not
lie about the cause.

### API Pitfalls

- Use `ssh_config.Decode(io.Reader)` (`config.go:329`) and the methods
  `*Config`. **`(*Config).Get` does not apply default values** — `config.go:375-402`
  ends in `return "", nil`. Defaults are only output via `UserSettings.Get` /
  `GetStrict`, which query `Default(keyword)` (`validators.go:14`). Checked:
  `cfg.Get("unlisted-host", "Port")` returns `""`, not `"22"`. `ngx` applies
  the defaults themselves (port 22, current user) after parse.
- `UserSettings` stores the result in a `sync.Once` (`config.go:271`). In testing,
  create a new `&ssh_config.UserSettings{}` per case and call `ConfigFinder`
  before the first `Get`, otherwise the cache from the previous case leaks.
- `Include` with relative path resolves against `~/.ssh` (or `/etc/ssh` if
  the file is system) using the library's own `homedir()`
  (`config.go:63-70`), which calls `os/user.Current()` and falls into `$HOME` if that
  fail. This is **not** `os.UserHomeDir()`, so both resolutions can
  diverge in an exotic environment. This is not a reason to change libraries; is
  reason for `ngx` not to assume that the two paths are the same.

## 6. `~/.ssh` path on three platforms

`os.UserHomeDir()` is fine. The source of Go 1.25.9
(`$GOROOT/src/os/file.go:605-624`) chooses the environment variable by
`runtime.GOOS`:

```go
env, enverr := "HOME", "$HOME"
switch runtime.GOOS {
case "windows":
        env, enverr = "USERPROFILE", "%userprofile%"
case "plan9":
        env, enverr = "home", "$home"
}
if v := Getenv(env); v != "" {
        return v, nil
}
```

On Windows it reads `%USERPROFILE%`, which is where Windows OpenSSH looks for the
`.ssh` — and, importantly for us, this **doesn't** depend on cgo or
`os/user.Current()`. On Linux and macOS it reads `$HOME`.

The path is assembled with `filepath.Join(home, ".ssh", "known_hosts")`:
`filepath` uses the native separator, so the same code produces
`/home/x/.ssh/known_hosts` and `C:\Users\x\.ssh\known_hosts`. Never concatenate
with `/` in hand.

Possible error: `os.UserHomeDir()` returns error if variable is empty. One
CI service on Windows without `%USERPROFILE%` crashes in this case; the message of
`ngx` should say that it did not find the user directory and that
`--known-hosts` / `--key` resolve rather than propagating `"%userprofile% is not
defined"`.

## Summary of decisions

An absent agent is not a mistake.
5. `~/.ssh/config`: `kevinburke/ssh_config` v1.6.0, honoring `Host` (wildcard and
   empty is unknown host, non-empty is **key changed and possible attack**.
   6. `~/.ssh`: `os.UserHomeDir()` + `filepath.Join`.2. Authentication: `ssh.PublicKeysCallback(agent.NewClient(conn).Signers)`, then
   negation), `Include`, `Match all` and `Match Host`, and reading `HostName`, `User`,
   `*RevokedError` is a third case. 1. `ssh-agent`: `SSH_AUTH_SOCK` first on every platform; Missing file is a room.
key in file, then password (from `NGX_SSH_PASSWORD` or from prompt in
   `Port` and `IdentityFile`. on Windows, fallback
   4. Remote read and glob: `sftp.Client.Open` and `sftp.Client.Glob`, aware of
   terminal — never flag).
Parse failing due to unsupported `Match` becomes
   to `\.\pipe\openssh-ssh-agent` via `go-winio`. that `Glob` swallows I/O errors.
3. Host key: `knownhosts.New`, with `*KeyError` sorted by `len(Want)` —
   explicit diagnosis, not silence.

No items require cgo.
