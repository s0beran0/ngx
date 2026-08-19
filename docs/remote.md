# Operating a remote host

`ngx` reads and inspects the configuration of a remote server over SSH.
Nothing is installed on the other side.

> **State.** The remote path is implemented, covered by tests, and **has been
> exercised against a real production nginx** — Oracle Linux 9, nginx 1.20.1,
> 132 configuration files — as well as against a throwaway container. That run
> is where several of the behaviours documented here came from: the
> authentication order, the privileged-read fallback, and the host-key
> diagnostics were all corrected because of what it found.
>
> What is *not* yet proven: Windows as the client, and any target that is not
> Linux with OpenSSH.

## Minimal usage

```console
$ ngx --host web1 inspect
```

If `ssh web1` already works on your machine, this works: `ngx` reads the same
`~/.ssh/config`, uses the same `ssh-agent` and checks the same
`~/.ssh/known_hosts`. No flag other than `--host`.

`--host` accepts either an alias from `~/.ssh/config` or an address. When it
is an alias, the `HostName` from the file translates the alias into the real
target — exactly as `ssh` does.

From `~/.ssh/config`, `ngx` honors **four keys**: `HostName`, `User`, `Port`
and `IdentityFile`. That is all. `ProxyJump`, `Match`, `Include` and the rest
are not applied. Honoring little and saying which little is better than
honoring badly in silence; if you depend on `ProxyJump`, `ngx` does not yet
serve that host.

Explicit flags beat the file, which beats the default:

```console
$ ngx --host 10.0.0.7 --user deploy --port 2222 --key ~/.ssh/id_ed25519 inspect -c /etc/nginx/nginx.conf
```

An empty flag is not a flag: `--user ""` does not erase the `User` that came
from the file. And any connection flag without `--host` is a usage error, not
a value silently ignored — whoever typed `--user deploy` believes the
connection will use that user:

```console
$ ngx --user deploy version; echo "exit=$?"
{"ok":false,"command":"version","schema_version":1,"ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0002","message":"--user only makes sense together with --host"}],"meta":{"duration_ms":0}}
exit=2
```

## Nothing is installed on the server

`ngx` runs entirely on your machine. On the remote side it uses two things any
server with OpenSSH already has:

- **SFTP**, to read the configuration files;
- **command execution**, to run the `nginx` that is **already there**, with an
  explicit argv — never a shell line built by concatenation.

No binary is copied, there is no agent, no directory is created, no package is
installed. Disconnecting leaves no trace. That is a design requirement, not a
consequence: a tool that has to install itself on every server in order to
read a `.conf` would not be usable on a customer's machine.

## Authentication

The order is always the same: **ssh-agent, then key file, then password**. All
available methods are offered to the server, which picks.

1. **`ssh-agent`.** If there is a reachable agent (`SSH_AUTH_SOCK`, or the
   default named pipe on Windows), its keys come first. The list is requested
   from the agent at authentication time, so a key added with `ssh-add` after
   `ngx` started is still seen.

   Not having `ssh-agent` **is not an error** — it is one method fewer,
   reported as an `info` severity diagnostic.

2. **Key file**, from `--key` or from `IdentityFile` in `~/.ssh/config`. If
   the key is encrypted, the passphrase comes from `NGX_SSH_KEY_PASSPHRASE` or
   from a prompt when standard input is a terminal. Under a pipe, without the
   variable, the method simply drops off the list with a warning that names
   the variable — instead of the command hanging, waiting for typing that will
   never come. That is what keeps `ngx` usable by an AI agent.

3. **Password**, from `NGX_SSH_PASSWORD` or from a prompt without echo on the
   terminal.

### There is no password flag, and that is deliberate

No password, and no passphrase, comes in through the command line. **There is
no `--password`**, and adding one should be rejected in review.

The reason is that a flag's value does not stay where you wrote it:

- it shows up in `ps` for **any user on the machine**, for as long as the
  process lives;
- it is recorded in the shell history, in plain text, forever;
- it goes in full into any CI log, and CI logs are read by far more people
  than whoever wrote the pipeline.

Whoever passes a password through a flag has already leaked it, even if the
command worked. The two accepted inputs — environment variable and prompt — do
not have that problem.

```console
$ NGX_SSH_PASSWORD='...' ngx --host web1 inspect
```

Even the environment variable deserves care: prefer a secrets manager that
injects it into the process, rather than an `export` in `.bashrc`.

## An unknown host is refused

`ngx` checks the server's key against `known_hosts` and **refuses the
connection** when it has nothing to compare against. There is no "accept and
carry on", and no interactive question that a script could answer `yes` to by
accident.

There are four outcomes, with distinct codes, so that the consumer of the
output can decide without interpreting text:

| Code | Situation |
|---|---|
| `NGX-0201` | unknown host — never seen before |
| `NGX-0202` | **the host key changed** — could be interception |
| `NGX-0203` | the presented key is marked `@revoked` |
| `NGX-0204` | the `known_hosts` file does not exist |

The missing-file case, for real:

```console
$ ngx --host 127.0.0.1 --port 2222 --known-hosts /tmp/does-not-exist-known-hosts --timeout 3s version; echo "exit=$?"
{"ok":false,"command":"version","schema_version":1,"ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0204","message":"/tmp/does-not-exist-known-hosts does not exist: ngx has no recorded key to compare with the one from 127.0.0.1. Run `ssh 127.0.0.1` once to record the host, point at another file with --known-hosts, or accept any key with --insecure-host-key (insecure)","file":"/tmp/does-not-exist-known-hosts"}],"meta":{"duration_ms":0}}
exit=1
```

An unknown host (`NGX-0201`) is the normal friction of first access, and the
message hands over **the ready-made line** to paste into `known_hosts`. A
changed key (`NGX-0202`) is something else entirely: the message says "this
could be a man-in-the-middle attack" in so many words, shows the presented key
next to the recorded ones, and points at the file and line of the entry that
diverges.

### Adding a host to `known_hosts`

Three paths, from best to worst:

```console
# 1. connect once through ssh itself, checking the fingerprint on screen
$ ssh web1

# 2. fetch the key and check the fingerprint against what the server publishes
$ ssh-keyscan -H web1 >> ~/.ssh/known_hosts

# 3. paste the line the NGX-0201 error message already handed you
```

`ssh-keyscan` on its own **verifies nothing** — it asks for the key from
whoever answers on port 22, which is exactly who an attacker would be
impersonating. It is only safe if you compare the fingerprint against an
independent source (the provider's console, the inventory, a colleague on the
phone).

If the key changed legitimately — server reinstalled — remove the old one with
`ssh-keygen -R web1` and register the new one.

### `--insecure-host-key`

Accepts the server's key **with no verification at all**. The connection stays
encrypted, but it is no longer protected against interception: any machine on
the route can impersonate the server, and `ngx` would have no way to notice.

```console
$ ngx --host 127.0.0.1 --port 2222 --insecure-host-key --timeout 3s version
{"ok":false,"command":"version","schema_version":1,"ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"warning","code":"NGX-0211","message":"--insecure-host-key: the host key of 127.0.0.1 will be accepted with no verification at all. The connection is not protected against interception (man-in-the-middle) and any machine on the route can impersonate the server"},{"severity":"error","code":"NGX-0206","message":"could not connect to eduardoborges@127.0.0.1:2222: dial tcp 127.0.0.1:2222: connect: connection refused. Authentication methods offered: ssh-agent"}],"meta":{"duration_ms":0}}
```

The `NGX-0211` warning appears in the output **every time**, alongside the
result, on success and on error. It cannot be suppressed, and that is the
intent: a silent security escape hatch stops being an escape hatch and becomes
the normal behavior.

**Do not let this become a habit.** The legitimate use is narrow: a disposable
test bench, a test container that gets a new host key on every `docker run`,
an ephemeral lab. If `--insecure-host-key` has shown up in your production
script, or in the alias you type every day, the problem is not the
verification — it is that your fleet's `known_hosts` is not being managed.
Distributing a fleet `known_hosts`, or using a host certificate signed by an
SSH CA, is what actually solves it.

## Privilege: `--sudo` is explicit

On a production server, the nginx configuration is usually readable only by
root, and `sudo` is usually allowed without a password. In other words: the
path that "just works" is the one that escalates privilege in silence.

`ngx` **does not do that**. If a remote command needs privilege, it only runs
with an explicit `--sudo`. Without the flag, `ngx` reports that the command
requires privilege and **which command it is** — it does not retry with
`sudo`, and it does not guess.

```console
$ ngx --host web1 --sudo test
```

A tool driven by an AI agent that escalates privilege on its own, on a
production server, turns a read error into a `root` command. The friction of
typing `--sudo` is the record that **somebody decided**.

Details that avoid surprises:

- `sudo` is invoked with `-n` (non-interactive), because `ngx` runs without a
  TTY. A `sudo` that asks for a password fails with its own diagnostic
  (`NGX-0222`) instead of hanging, waiting for typing.
- `--sudo` applies to the **local** target too. Explicit privilege is not a
  rule of the remote path; it is a rule of `ngx`.
- **`--sudo` applies to whoever runs nginx, not to whoever reads files.** The
  `test` and `status` commands run the binary (`nginx -t`, `nginx -V`) and
  switch to running `sudo -n nginx ...` when the flag is present. `inspect`
  does not: it reads the files over SFTP, and reading over SFTP does not go
  through `sudo` — it happens with the permissions of the user who connected.
  If `/etc/nginx/nginx.conf` is not readable by them, `inspect` fails to open
  the file, and `--sudo` does not help. The short-term way out is to connect
  with a user that has read access.

## On Windows, enable the `ssh-agent` service

Windows 10/11 and Windows Server already ship OpenSSH, but the `ssh-agent`
service comes **disabled out of the box**. `ngx` talks to the named pipe
`\\.\pipe\openssh-ssh-agent` — the same one `ssh.exe` uses — and, with the
service stopped, the pipe simply does not exist: `ngx` reports the agent as
unavailable (`info` severity, not an error) and moves on to key file and
password.

To enable it, in a PowerShell **as administrator**:

```powershell
Get-Service ssh-agent | Select-Object Name, Status, StartType
Set-Service -Name ssh-agent -StartupType Automatic
Start-Service ssh-agent
```

Then, in your normal terminal:

```powershell
ssh-add $env:USERPROFILE\.ssh\id_ed25519
ssh-add -l
```

If `SSH_AUTH_SOCK` is set — because you use 1Password, `gpg-agent` or a WSL
relay — it takes precedence over the default pipe. It is the same order
Windows OpenSSH applies, so an alternative agent that already works with
`ssh.exe` works with `ngx` without configuration.

## The latency caveat

**Every `include` in the configuration is a network read.** Locally, reading a
file costs microseconds and nobody thinks about it; over SSH, each one pays
the link's latency.

Measured on a real production nginx — Oracle Linux 9, nginx 1.20.1, access
over VPN — the effective configuration had **132 files**. That is 132 network
reads to build a tree, and today they happen **in sequence**: there is no
parallelism in include resolution. On a link with 50 ms round trip, the
round-trips alone add up to more than six seconds, before a single byte of
content.

What this means in practice:

- `ngx --host ... inspect` on a large configuration **is not interactive**.
  Adjust `--timeout` (default 30s) accordingly, and do not put it inside a
  loop.
- A small configuration answers quickly; the cost grows with the number of
  files, not with their size.
- Parallelizing the read by tree level is the obvious optimization and it is
  planned. Until it exists, the number above is what you should expect.

There is one more correctness detail that latency hides: `ngx` implements its
own remote glob on top of `ReadDir`, instead of using `Glob` from `pkg/sftp`,
because that one **discards I/O errors by contract**. With it, an
`include /etc/nginx/conf.d/*.conf` on an unstable link would return zero files
and no error — and `ngx` would present the server's configuration without the
files it actually has, as if the server genuinely did not have them. A tool
read by an AI agent cannot be confidently incomplete: the consumer has no way
to suspect it.

## What does not exist yet

- Multiple hosts in one call (`--hosts a,b,c`).
- `ProxyJump`, bastion and tunnel.
- Remote transactional writing — depends on the v0.2 mutation commands.
- Parallel reading of includes.
- `ngx --host web1 version` **opens an SSH connection** to print a purely
  local string. It is slow and can fail for reasons unrelated to the command;
  it is recorded as a known limitation.
