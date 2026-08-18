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
{"ok":false,"command":"inspect","ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0002","message":"--no-redact so e aceito quando a saida e um terminal"}],"meta":{"duration_ms":0,"target":"local"}}
```

A human who asks to see the secret sees it on screen. An agent reading the
pipe, structurally, cannot even ask.

## Current state — read this before trying to install

This is **v0.1, under development**, and it is **read-only**. Nothing here
changes the nginx configuration.

- **There are only pre-releases.** The stable channel is still empty, so
  `install.sh` without `NGX_CHANNEL=beta` answers that only pre-releases exist
  and points at beta. Releases are signed with minisign, and the installer
  REFUSES to install when it cannot verify the signature — absence of
  verification is a failure, never a "carried on anyway". When minisign is not
  installed, it verifies with `openssl`, which exists on practically every
  server.

  ```sh
  curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh \
    | NGX_CHANNEL=beta sh
  ```
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
{"ok":true,"command":"version","ngx_version":"0.1.0-dev","data":{"version":"0.1.0-dev"},"diagnostics":[],"meta":{"duration_ms":0,"target":"local"}}
```

Copy `bin/ngx` wherever you want — it is a static binary, no installer needed.

Other useful targets: `make test`, `make test-race`, `make lint`, `make
verificar` (what CI runs) and `make ajuda` for the full list.

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

## Using it today

### `ngx inspect`

Reads the configuration and returns the whole tree plus a summary. The path
comes from `-c`/`--config`, or from `nginx.config` in `ngx`'s own
configuration file.

```console
$ ./bin/ngx inspect -c internal/cli/testdata/exemplo.conf | jq -c '.data.summary'
{"files":1,"servers":1,"locations":2,"upstreams":1}
```

Every node in the tree carries `directive`, `args`, `file`, `line`, `column`,
the byte offsets (`span` and `head_span`) and a stable `id` — `h.s0.l0` is the
first `location` of the first `server` inside `http`. The envelope also brings
`meta.config_hash`, the canonical hash of the configuration that was read.

Sensitive values come out redacted by default:

```console
$ ./bin/ngx inspect -c internal/cli/testdata/exemplo.conf | jq -c '.data.config[0].parsed[1].block[0].block[2]'
{"directive":"ssl_certificate_key","args":["***"],"file":"internal/cli/testdata/exemplo.conf","line":7,"column":9,"span":{"start":100,"end":145},"head_span":{"start":100,"end":144},"id":"h.s0.d2"}
```

`--combine` resolves the `include`s into a single tree instead of a list of
files.

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
$ ./bin/ngx inspect -c internal/cli/testdata/invalido.conf; echo "exit=$?"
{"ok":false,"command":"inspect","ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0003","message":"internal/cli/testdata/invalido.conf:5: unexpected end of file, expecting \"}\" in internal/cli/testdata/invalido.conf:5","file":"internal/cli/testdata/invalido.conf","line":5}],"meta":{"duration_ms":0,"target":"local"}}
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
```

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
