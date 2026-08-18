# Test bench

A disposable container with `sshd` and `nginx`, used as the target of the
integration tests for `ngx`'s remote path. It is a versioned artifact of the
repository: whoever clones the project brings the bench up with a single
command, on Linux or macOS, with nothing installed beyond Docker and `ssh`.

```sh
make bancada-up      # generates the key, builds the image and starts the container
make bancada-smoke   # proves, one by one, the required properties
make bancada-down    # tears down the container, removes the image and deletes the key
```

Helpers: `make bancada-logs` (the `sshd` log) and `make bancada-shell` (an
interactive session as `ngxtest`).

## How to connect

| | |
|---|---|
| Address | `127.0.0.1`, port **2222** (fixed; `make BANCADA_PORTA=2223 bancada-up` to change it) |
| User | `ngxtest`, uid 1000, **non-root** |
| Private key | `test/bancada/.chave/id_ed25519` (ed25519, no passphrase) |
| Privilege | `sudo -n /usr/sbin/nginx`, no password, nginx **only** |
| nginx | 1.20.1, Oracle Linux 9 |

The port listens on `127.0.0.1` only. The key is **generated** by
`make bancada-up` and never enters git (`test/bancada/.chave/` is in
`.gitignore`); the container's host keys are generated on every startup, so
that the test for refusing an unknown host key does not trip over a
`known_hosts` left by the previous run.

## What the bench reproduces

The shape was measured on a real production nginx (Oracle Linux 9, nginx
1.20.1), and that is why the base image is `oraclelinux:9`: this is the family
that brings the layout with three include directories and an nginx compiled
with `--modules-path`, which gives the third wildcard without a hack. The
`nginx` appstream module is disabled at build time so that the non-modular
package wins, which is precisely 1.20.1 — without that, the day the default
stream turns 1.26 the bench would change on its own. `gerar-config.sh` aborts
if the version is not 1.20.x.

**Three wildcard patterns**, which only resolve inside the container:

| Directive | Context | Files |
|---|---|---|
| `include /usr/share/nginx/modules/*.conf;` | top level | 4 (`nginx-mod-*` packages) |
| `include /etc/nginx/conf.d/*.conf;` | `http` | 112 |
| `include /etc/nginx/default.d/*.conf;` | `server` | 12 |

**130 files in the effective configuration**, counting `nginx.conf` and
`mime.types`. The number is checked in the build itself: `gerar-config.sh`
runs `nginx -T` and fails if the total does not match. It is the volume that
makes sequential latency visible — a target with three files passes everything
and proves nothing.

**`nginx -T` readable only by root.** `/etc/nginx` is `0700 root:root` and the
files are `0600`, so neither `nginx -T` nor a read over SFTP sees the
configuration as `ngxtest`. Passwordless `sudo` exists, but restricted to the
nginx binary: it is the trap from DR5, the target where a client that
escalated on its own would go unnoticed.

**A secret inside the configuration**, in three forms, to exercise redaction
end to end:

- a literal token in the text — `proxy_set_header Authorization "Bearer ngx-bancada-token-4f3c9a1b2e";`
  in `conf.d/05-privado.conf`. It is the only one that shows up in the
  `nginx -T` dump, and therefore the one redaction has to catch;
- `auth_basic_user_file /etc/nginx/secrets/htpasswd;`
- `ssl_certificate_key /etc/nginx/secrets/tls.key;` — a real RSA key,
  generated at build time, because `nginx -T` loads the pair and a fake file
  would break the dump.

None of these secrets is real, and none leaves the container.

## The glob trap

`armadilha-local/etc/nginx/conf.d/zz-armadilha-local.conf` is a file with the
same directory name used inside the container, but **on the local disk**. It
only enters a tree read from the bench if the parser's `Glob` is not injected
with the remote filesystem — the defect Task R3 fixed.

The marker `ARMADILHA-LOCAL-NAO-DEVE-APARECER` must never appear in an
effective configuration read from the container; the smoke test verifies this,
and the Go integration test must point its local filesystem at that directory
when exercising the case.

## Files

| File | Role |
|---|---|
| `Dockerfile` | image: Oracle Linux 9, nginx 1.20.1, `sshd`, `sudo`, user `ngxtest` |
| `sshd_config.bancada` | `sshd` by key only; `PermitRootLogin no`, no password, with SFTP |
| `gerar-config.sh` | generates the 130 files at build time and checks the total |
| `entrypoint.sh` | installs the public key, generates host keys, starts nginx and `sshd` |
| `smoke.sh` | proves the properties above against the running container |
| `armadilha-local/` | local namesake for the glob test |

The container is disposable and runs locally only: it has no hardening, and it
should not be exposed. What it does not have, on purpose: a root password,
root login, and password authentication.
