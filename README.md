# ngx

A Go CLI that makes nginx operable by programs: structured JSON output,
reading by selector, and — once v0.2 lands — transactional changes with
rollback.

## Two audiences, one tool

`ngx` is built to be used by **AI agents** and by **humans**, and the output
adapts on its own: when stdout is **not** a terminal, it is JSON; when it is,
it is readable. `--json` and `--human` force one of the two.

This is not decoration. An agent reading a pipe needs structure to decide; a
person debugging needs text. The same invocation serves both without anyone
having to remember a flag.

Where the behavior diverges, the divergence is a safety rule. `--no-redact`,
which turns off the hiding of sensitive values, is only accepted on a
terminal:

```console
$ ngx --no-redact inspect -c nginx.conf | cat
{"ok":false,"command":"inspect","schema_version":1,"ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0002","message":"--no-redact is only accepted when the output is a terminal"}],"meta":{"duration_ms":0,"target":"local"}}
```

A human who asks to see the secret sees it on screen. An agent reading the
pipe, structurally, cannot even ask.

## Current state — read this before trying to install

This is **v0.1, under development**. It is read-only **as a milestone, not as
a design**: `ngx` is meant to edit and create `.conf` files, and v0.2 brings
mutation with plan/apply and rollback.

v0.1 ships without any write path on purpose. The two riskiest bets of the
project — the selector language and the stability of node IDs — get validated
first, so that when a code path capable of writing to a production `.conf`
finally exists, it is built on parts that were already proven. Shipping the
writer first would mean discovering a parser bug by corrupting somebody's
server.

The architecture already carries what writing needs: every node holds byte
spans (`Span` for the whole directive, `HeadSpan` for name and arguments),
which is what makes a v0.2 edit a byte substitution instead of a re-render of
the file — comments, blank lines and the author's formatting survive
untouched. IDs are anchored to a `config_hash` so an agent cannot apply a
change against a tree that moved underneath it.

So: today nothing here changes the nginx configuration.

- **v0.1.0 is the first stable release.** Releases are signed with minisign,
  and the installer REFUSES to install when it cannot verify the signature —
  absence of verification is a failure, never a "carried on anyway". When
  minisign is not installed, it verifies with `openssl`, which exists on
  practically every server.

  ```sh
  curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh | sh
  ```

  `NGX_CHANNEL=beta` picks up pre-releases instead. The script installs into
  `/usr/local/bin` and tells you the exact command to re-run with privilege if
  the directory needs it — it never calls `sudo` on its own. Point it somewhere
  else with `NGX_INSTALL_DIR`.
- **There are five commands:** `version`, `inspect`, `test`, `status` and
  `update`. Commands foreseen in the design (`get`, `tree`, `fmt`, `diff`,
  `apply`) do not exist yet. `status` does not detect drift yet, so it never
  exits with the code 7 the design reserves for it.
- **Remote access over SSH exists and has been exercised against a real
  production nginx** (Oracle Linux 9, nginx 1.20.1, 132 configuration files),
  besides the containerized test bench. See [`docs/remote.md`](docs/remote.md).
- **The "human" output is still raw:** today it is the JSON of the data
  formatted with indentation, and the error as a line of text. Improving this
  is pending work, not a deliberate style.

## Building from source

Requires Go 1.25 or newer. No CGO, no system dependency.

```console
$ make build
CGO_ENABLED=0 go build -o bin/ngx ./cmd/ngx

$ ./bin/ngx version
{"ok":true,"command":"version","schema_version":1,"ngx_version":"0.1.0-dev","data":{"version":"0.1.0-dev"},"diagnostics":[],"meta":{"duration_ms":0,"target":"local"}}
```

Copy `bin/ngx` wherever you want — it is a static binary, no installer needed.

Other useful targets: `make test`, `make test-race`, `make lint`, `make
verify` (what CI runs) and `make help` for the full list.

### The installers

`install.sh` (Linux and macOS) and `install.ps1` (Windows) are in the
repository and work today, against the pre-releases: the stable channel is
empty, so `NGX_CHANNEL=beta` is required. Their `--help` is the correct
reference for the variables:

```console
$ sh install.sh --help
install.sh — the ngx installer for Linux and macOS
...
```

| Variable | Effect |
|---|---|
| `NGX_INSTALL_DIR` | installation directory (`/usr/local/bin`; `%LOCALAPPDATA%\ngx\bin` on Windows) |
| `NGX_CHANNEL` | `stable` (default) or `beta`, which includes `-rc`/`-beta`/`-alpha` |
| `NGX_VERSION` | pinned version, e.g. `v0.2.0`; when set, the GitHub API is not queried |
| `NGX_ALLOW_UNVERIFIED` | `1` allows installing when the signature **cannot** be verified; it never ignores an invalid signature or a mismatched checksum |

Neither of them calls `sudo` on its own: if the directory requires privilege,
the script prints the exact line to run and stops. Escalating privilege by
itself inside something executed through `curl | sh` is exactly what nobody
should agree to run.

The SHA256 checksum is always checked and there is no way to turn it off.

### Installing from a package manager

`curl | sh` comes first in this list because it is the one that works
everywhere today. The rest are there for whoever already lives in a package
manager:

| Channel | Command | Self-updates? |
|---|---|---|
| installer | `curl -fsSL .../install.sh \| sh` | yes, `ngx update` |
| Homebrew (macOS) | `brew install s0beran0/tap/ngx` | no — `brew upgrade ngx` |
| Debian/Ubuntu | `dpkg -i ngx_*_linux_amd64.deb` | no — `apt upgrade ngx` |
| Fedora/RHEL | `rpm -i ngx_*_linux_amd64.rpm` | no — `dnf upgrade ngx` |
| Alpine | `apk add --allow-untrusted ngx_*.apk` | no — `apk upgrade ngx` |
| Scoop | `scoop install ngx` | no — `scoop update ngx` |
| WinGet | `winget install EduardoBenck.ngx` | no — `winget upgrade ngx` |
| Arch (AUR) | `yay -S ngx-bin` | no — `pacman -Syu ngx` |

**A packaged `ngx` does not update itself, on purpose.** The binary knows which
channel installed it — `ngx version` reports `install_channel` — and when that
channel is a package manager, `ngx update` refuses and names the command that
does the job instead. Replacing a file that `apt`, `brew` or `scoop` believes
it owns leaves their database describing something that is no longer there, and
the next upgrade either reverts you in silence or fails. So a refusal from
`ngx update` is the tool working, not the tool broken.

[`docs/install-channels.md`](docs/install-channels.md) has the full table, what
is automatic on each release and what still needs a human — WinGet, notably,
goes out as a pull request to `microsoft/winget-pkgs` that Microsoft reviews.

## Using it today

### The envelope

Every answer — success or failure — comes in the same envelope:

```console
$ ./bin/ngx version
{"ok":true,"command":"version","schema_version":1,"ngx_version":"0.1.0-dev","data":{"version":"0.1.0-dev"},"diagnostics":[],"meta":{"duration_ms":0,"target":"local"}}
```

`schema_version` is the field to branch on. It describes the **shape** of the
output and is a plain integer, not semver: compare it with `>=` and there is
nothing to parse wrong. It moves only when a change breaks whoever reads the
output — a field renamed or removed, a type changed, the meaning of a field
changed. A field being *added* never moves it, which is why adding one is safe.
`ngx_version` cannot do this job: it changes on every release, including the
ones that change nothing about the output.

It is present in the failure envelope too — see [Errors](#errors) — because an
agent that only ever sees errors still needs to know which shape it is reading.

### `ngx inspect`

Reads the configuration and returns a summary of it. The path comes from
`-c`/`--config`, or from `nginx.config` in `ngx`'s own configuration file.

```console
$ ./bin/ngx inspect -c internal/cli/testdata/filters/nginx.conf
{"ok":true,"command":"inspect","schema_version":1,"ngx_version":"0.1.0-dev","data":{"summary":{"files":3,"servers":4,"locations":1,"upstreams":0}},"diagnostics":[],"meta":{"duration_ms":0,"config_hash":"sha256:1d0e29385ff4b3649329528a413898feefd0f1b02849684fe239c15e1c756eea","target":"local"}}
```

**The tree is not the default.** On a production nginx it is 1.6 MB of JSON,
which is a context budget spent to answer a question about one file. Three
flags ask for it, and `data.config` is absent — not `[]` — until one of them
does:

| Flag | What comes out |
|---|---|
| `--file <fragment>` | only that file |
| `--server <name>` | only the `server` blocks with that `server_name` |
| `--full-tree` | everything, and the name says what it costs |

Every node in the tree carries `directive`, `args`, `file`, `line`, `column`,
the byte offsets (`span` and `head_span`) and a stable `id` — `h.s0.l0` is the
first `location` of the first `server` inside `http`. The envelope also brings
`meta.config_hash`, the canonical hash of the configuration that was read.

Sensitive values come out redacted by default:

```console
$ ./bin/ngx inspect --full-tree -c internal/cli/testdata/example.conf | jq -c '.data.config[0].parsed[1].block[0].block[2]'
{"directive":"ssl_certificate_key","args":["***"],"file":"internal/cli/testdata/example.conf","line":7,"column":9,"span":{"start":100,"end":145},"head_span":{"start":100,"end":144},"redacted_args":[0],"id":"h.s0.d2"}
```

`redacted_args` holds the indices of the arguments that were replaced. It has
to be there because a configuration is allowed to contain three asterisks of its
own, and then the censored value and the real one are the same string:

```console
$ ./bin/ngx inspect --full-tree -c internal/cli/testdata/redaction.conf | jq -c '[.. | objects | select(.directive? == "proxy_set_header")]'
[{"directive":"proxy_set_header","args":["Authorization","***"],"file":"internal/cli/testdata/redaction.conf","line":9,"column":13,"span":{"start":143,"end":196},"head_span":{"start":143,"end":195},"redacted_args":[1],"id":"h.s0.l0.d0"},{"directive":"proxy_set_header","args":["X-Masked-Upstream","***"],"file":"internal/cli/testdata/redaction.conf","line":10,"column":13,"span":{"start":209,"end":248},"head_span":{"start":209,"end":247},"id":"h.s0.l0.d1"}]
```

Both print `***`. The first one was censored, and `redacted_args` says at which
argument; the second one is what the file really says, and carries no mark at
all — the field is omitted when nothing was redacted, never sent as an empty
list. The header name stays visible, so an agent can still tell which header it
is not being shown.

Redaction is applied when the output is written, never to the tree in memory:
that is what keeps a future `ngx fmt` from writing `***` into the file.

`--combine` resolves the `include`s into a single tree instead of a list of
files.

#### Finding a file: `--file` and `--server`

`--file` matches against the **whole path**, never against the base name. A
fragment matches by substring; a value starting with `/` is an absolute path
and matches exactly. There is no globbing rule to learn.

```console
$ ./bin/ngx inspect --file sites/portal.conf -c internal/cli/testdata/filters/nginx.conf | jq -c '.data.config[0].parsed[1] | {directive, args, id}'
{"directive":"server","args":[],"id":"s1"}
```

Matching the whole path is what makes the same base name in two directories
come out as a question instead of a guess. Several matches is a refusal with
the candidates named, and exit 2 — `ngx` never picks one:

```console
$ ./bin/ngx inspect --file portal.conf -c internal/cli/testdata/filters/nginx.conf; echo "exit=$?"
{"ok":false,"command":"inspect","schema_version":1,"ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0101","message":"--file \"portal.conf\" matches 2 files: internal/cli/testdata/filters/sites/portal.conf, internal/cli/testdata/filters/sites-extra/portal.conf. Use a longer fragment, or the absolute path, which matches exactly"}],"meta":{"duration_ms":0,"target":"local"}}
exit=2
```

No match is also a refusal, and it says what **was** there — an empty result
and a misspelt name look identical otherwise:

```console
$ ./bin/ngx inspect --file backend -c internal/cli/testdata/filters/nginx.conf | jq -r '.diagnostics[0].message'
--file "backend" matches no file. Read: internal/cli/testdata/filters/nginx.conf, internal/cli/testdata/filters/sites/portal.conf, internal/cli/testdata/filters/sites-extra/portal.conf
```

`--server` works the same way over `server_name`, with one addition: an exact
`server_name` wins outright, so `--server example.com` is not made ambiguous by
the existence of `api.example.com`. With no exact hit the value is a fragment,
and several names is the same refusal as above (`NGX-0103`):

```console
$ ./bin/ngx inspect --server portal -c internal/cli/testdata/filters/nginx.conf | jq -r '.diagnostics[0].message'
--server "portal" matches 2 server names: portal.example.com, portal-admin.example.com. Use a longer fragment; an exact server_name matches only itself
```

Ambiguity is over the **name**, not over the number of blocks: one
`server_name` served by a `:80` block and a `:443` block is ordinary nginx, and
both blocks come out.

The two flags combine with **AND**. `--file a.conf --server x.example.com`
means "the `x.example.com` blocks that live in `a.conf`", never their union —
and the names offered when nothing matches are the ones of the narrowed scope:

```console
$ ./bin/ngx inspect --file sites-extra/portal.conf --server portal.example.com -c internal/cli/testdata/filters/nginx.conf; echo "exit=$?"
{"ok":false,"command":"inspect","schema_version":1,"ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0104","message":"--server \"portal.example.com\" matches no server_name. Declared: portal-admin.example.com"}],"meta":{"duration_ms":0,"target":"local"}}
exit=2
```

Both flags filter what is **emitted**, not what is read. `--file` could prune
the read as well and does not yet; `--server` structurally never can, because
knowing which file declares a `server_name` requires reading the file.

#### A filtered answer says so, and drops the hash

A filtered tree is a subset by design, and the marker lives inside `data`,
where an agent reading only `data` trips over it while looking at `config`:

```console
$ ./bin/ngx inspect --file sites/portal.conf -c internal/cli/testdata/filters/nginx.conf | jq -c '.data.scope, .meta'
{"partial":true,"filters":{"file":"sites/portal.conf"},"files_emitted":1,"config_hash_omitted":true}
{"duration_ms":0,"target":"local"}
```

`meta.config_hash` is **omitted**, and that is the point. The hash is computed
over the tree it is given, so the hash of a filtered tree is a valid hash *of a
subset* and indistinguishable from the hash of the whole. Harmless while
reading, and a corrupted write the moment a future `ngx apply` uses it for
optimistic locking — so a filtered answer never carries one, and
`scope.config_hash_omitted` says why instead of leaving the caller to guess.

`data.summary` still describes the **whole** configuration that was read (three
files above, of which one came out), which is what makes it comparable between
a filtered call and an unfiltered one. `scope.files_emitted` is how much came
out.

### `ngx test`

Runs `nginx -t` on the target and returns the verdict with each diagnostic at
the file and line nginx reported:

```console
$ ./bin/ngx test | jq -c '.data.ok, .diagnostics[0]'
false
{"severity":"error","code":"NGX-0224","message":"invalid number of arguments in \"listen\" directive","file":"/etc/nginx/conf.d/app.conf","line":12}
```

A configuration that fails the check exits with **code 3, and the full
envelope**: failing is the answer to the question that was asked, not an
infrastructure failure. A real failure — no nginx on the target, missing
privilege, the connection dropped — exits with code 1 and the corresponding
diagnostic (`NGX-0220`, `NGX-0221`, `NGX-0222`).

### `ngx status`

Joins what the binary says about itself (`nginx -V`) with the state of the
process:

```console
$ ./bin/ngx status | jq -c '.data.nginx.version, .data.process'
"1.24.0"
{"running":true,"master_pid":4242,"pid_file":"/var/run/nginx.pid"}
```

What nginx does not report **is omitted, never estimated**. A build without
`--pid-path` does not say where the pidfile lives: `pid_file` and `running`
disappear from the JSON and a diagnostic explains the absence, instead of
`ngx` guessing a path or reporting `running: false` — which would claim,
without evidence, that nginx is down. The same goes for a pid owned by another
user, which exists but cannot be queried.

### `ngx update`

Updates `ngx` itself from the signed releases. It downloads, checks the
minisign signature and the SHA-256 checksum, and **only then** swaps the
binary: a verification failure leaves the current `ngx` untouched.

```sh
ngx update --check                  # only reports whether a new version exists
ngx update --channel beta           # includes pre-releases
ngx update --version v0.1.0-rc.1    # exact version, downgrade included
```

The channel comes from the flag, or from `NGX_CHANNEL`, or defaults to
`stable`. The variable exists because `install.sh` already uses it: whoever
installed through beta stays on beta without repeating the flag.

The command ignores `--host` on purpose. Updating `ngx` is about the machine
where it runs; nothing is installed on the remote server.

### `ngx version`

```console
$ ./bin/ngx version | jq -c .data
{"version":"0.1.0-dev"}
```

### Errors

A syntax error in the configuration points at file and line, and exits with
code 3:

```console
$ ./bin/ngx inspect -c internal/cli/testdata/invalid.conf; echo "exit=$?"
{"ok":false,"command":"inspect","schema_version":1,"ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0003","message":"internal/cli/testdata/invalid.conf:5: unexpected end of file, expecting \"}\" in internal/cli/testdata/invalid.conf:5","file":"internal/cli/testdata/invalid.conf","line":5}],"meta":{"duration_ms":0,"target":"local"}}
exit=3
```

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | internal or environment failure |
| 2 | usage error (invalid flag or command) |
| 3 | invalid nginx configuration |

Codes 7 (drift) and 9 (hash mismatch) are reserved for the mutation commands
of v0.2.

The exit code says how bad it was; `diagnostics[].code` says what happened, and
is the field to branch on. It is allocated by range: `0001`–`0009` generic and
aligned to the exit code of the same number, `0100`–`0199` configuration,
`0200`–`0299` transport and SSH, `0300`–`0399` update and distribution. The
`inspect` filters use the configuration range, all of them on exit 2 — the
invocation named nothing, and neither `ngx` nor the `.conf` is at fault:

| Code | Condition |
|---|---|
| `NGX-0101` | `--file` names more than one file |
| `NGX-0102` | `--file` names no file |
| `NGX-0103` | `--server` names more than one `server_name` |
| `NGX-0104` | `--server` names no `server_name` |
| `NGX-0105` | *info*: the answer is a filtered subset |

### Global flags

```
-c, --config          main nginx configuration
    --json/--human    force the output format
-q, --quiet           errors only
    --no-color        turn off colors
    --nginx-bin       path to the nginx binary
    --nginx-version   assume this nginx version
    --timeout         operation timeout (default 30s)
    --profile         profile from ngx's configuration file
    --no-redact       show sensitive values (terminal only)
    --field           print a single value from the envelope, by dot path
    --query           apply a jq expression to the envelope, one line per result
```

#### `--field`: one value comes out as one value

`--field` takes a dot path over the envelope — the same shape `--json`
prints — and writes that single value, raw: no quotes, no envelope, no JSON
parser needed on the other side. It lives in the renderer, so it works for
every command.

```console
$ ngx --field data.version version
0.1.0-dev
```

A path that does not exist is **exit 2 with nothing on stdout**. The
diagnostic goes to stderr, like any usage error:

```console
$ ngx --field data.nginx.version version 2>/dev/null
$ ngx --field data.nginx.version version
--field: the envelope has no value at "data.nginx.version"
$ echo $?
2
```

That empty stdout is the point: an empty line would be assigned by
`V=$(ngx --field ... status)` and the script would carry on believing it
worked.

It reads the failure envelope just the same, which is how a script gets the
code of what went wrong without parsing anything:

```console
$ ngx --field diagnostics.0.code inspect -c /does/not/exist.conf
NGX-0001
```

A string comes out raw; an object or a list, which have no raw form, come out
as compact JSON on one line. Redaction is applied before the selection, so
`--field` is not a way around it. `--field` is refused together with `--json`,
`--human` or `--quiet`: the first two ask for the whole envelope at the same
time as one field, and the third would suppress exactly the value that was
asked for.

#### `--query`: jq, embedded, for everything `--field` cannot address

`jq` was **not installed** on the production host this project was validated
against — neither was `minisign`. A tool that operates a remote server should
not require installing a second one to read its answer. So the evaluator
([`gojq`](https://github.com/itchyny/gojq), pure Go, MIT) ships inside the
binary: `--query` works on a machine with nothing else on it.

`--field` addresses **one** value by a fixed path. The moment the question is
"all of them", it has no answer — and that is most questions about an nginx
configuration:

```console
$ ./bin/ngx --field data.summary.servers inspect -c internal/cli/testdata/filters/nginx.conf
4
$ ./bin/ngx --query '.. | objects | select(.directive == "listen") | .args[0]' \
    inspect --full-tree -c internal/cli/testdata/filters/nginx.conf
80
80
443
8080
```

There is no dot path that produces those four lines. The expression can also
join fields across the tree, which is how "which file declares which name" is
answered in one call:

```console
$ ./bin/ngx --query '.. | objects | select(.directive == "server_name") | "\(.file)\t\(.args[0])"' \
    inspect --full-tree -c internal/cli/testdata/filters/nginx.conf
internal/cli/testdata/filters/nginx.conf	legacy.example.com
internal/cli/testdata/filters/sites/portal.conf	portal.example.com
internal/cli/testdata/filters/sites/portal.conf	portal.example.com
internal/cli/testdata/filters/sites-extra/portal.conf	portal-admin.example.com
```

**The expression runs on the redacted envelope**, never on the tree in memory.
Redaction happens in the renderer, so a query that reads the tree directly
would be a new flag walking around the whole redactor. Pointing `--query`
straight at a private key returns what `--json` would have returned:

```console
$ ./bin/ngx --query '.. | objects | select(.directive == "ssl_certificate_key") | .args[0]' \
    inspect --full-tree -c internal/cli/testdata/redaction.conf
***
```

`--no-redact` still governs that, and still only on a terminal.

Output follows `--field`'s rules, because it is the same code: a scalar comes
out raw with no quotes, and an object or a list comes out as compact JSON. What
is new is that gojq may produce **several** values — one line per value, always.

A valid expression that matches nothing is **exit 0 with an empty stdout**.
That is deliberate and it is *not* `--field`'s missing-path behaviour: in jq's
semantics a wrong path yields `null`, which is a line, so nothing is only ever
produced by a filter that deliberately excluded everything — "no server
matches" is an answer, and failing on it would break every legitimate
zero-match query under `set -e`.

```console
$ ./bin/ngx --query '.data.sumary' inspect -c internal/cli/testdata/filters/nginx.conf
null
$ ./bin/ngx --query '.. | objects | select(.directive == "grpc_pass") | .args[0]' \
    inspect --full-tree -c internal/cli/testdata/filters/nginx.conf
$ echo $?
0
```

One line per value is what makes that readable: zero lines means zero values.
A caller that needs a value even in the empty case wraps the expression —
`--query '[ ... ] | length'` always prints a number.

An expression that does not parse is a usage error, with gojq's own message,
refused before the command even runs:

```console
$ ./bin/ngx --query '.data |' inspect -c internal/cli/testdata/filters/nginx.conf
{"ok":false,"command":"inspect","schema_version":1,"ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0002","message":"--query: unexpected EOF"}],"meta":{"duration_ms":0}}
$ echo $?
2
```

The refusal comes out as a whole envelope, not through the broken expression —
filtering it through the very thing that is wrong would leave no envelope at
all.

An expression that parses but fails while running (indexing a string, say) is
exit 2 as well, with **nothing** on stdout — never a half-written answer:

```console
$ ./bin/ngx --query '.command.length' inspect -c internal/cli/testdata/filters/nginx.conf
--query: expected an object but got: string ("inspect")
$ echo $?
2
```

`halt_error` lands there too. A query never chooses ngx's exit code: that code
says what happened to the nginx operation, and letting an expression overwrite
it would make a successful read indistinguishable from a failed one.

Like `--field`, it reads the **failure** envelope just the same, and the exit
code stays the failure's:

```console
$ ./bin/ngx --query '.diagnostics[] | "\(.code)\t\(.message)"' inspect -c /does/not/exist.conf
NGX-0001	while parsing /does/not/exist.conf: open /does/not/exist.conf: no such file or directory
$ echo $?
1
```

`--query` is refused together with `--field` (two projections of the same
output, no coherent answer) and with `--json`, `--human` or `--quiet`, for the
same reasons `--field` is.

The remote access flags (`--host`, `--user`, `--port`, `--key`,
`--known-hosts`, `--insecure-host-key`, `--sudo`) are documented in
[`docs/remote.md`](docs/remote.md).

`--sudo` governs the execution of the nginx binary and applies to the local
target as well: `test` and `status` run `nginx -t` and `nginx -V` with
`sudo -n` when the flag is present. Without it, a command that needs privilege
is **reported**, with the exact line to authorize — `ngx` never repeats the
command with `sudo` on its own. `inspect` stays out of this: it reads files,
and reading (local or over SFTP) does not go through `sudo`.

## ngx's own configuration

`/etc/ngx/ngx.yaml` (global) and `.ngx/config.yaml` (local, relative to the
working directory). The local one overrides the global one, key by key:

```yaml
nginx:
  binary: /usr/sbin/nginx
  config: /etc/nginx/nginx.conf
output:
  format: auto      # auto, json or human
  redact:
    - ssl_certificate_key
```

The redaction list already comes filled with a default set; declaring it here
replaces that set.

## Remote access

`ngx` operates a remote host over SSH without installing anything on the
server:

```console
$ ngx --host web1 inspect
```

Authentication, host key verification, privilege and the latency caveat are in
[`docs/remote.md`](docs/remote.md).

## License

MIT. See [LICENSE](LICENSE).
