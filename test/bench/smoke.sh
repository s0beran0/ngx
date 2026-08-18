#!/usr/bin/env bash
# Proves, one by one, the properties the bench has to have.
# Does not depend on the ngx binary: it validates the TARGET, not the client.
#
# Usage: test/bench/smoke.sh [port] [key-path]
set -uo pipefail

PORT="${1:-${NGX_BENCH_PORT:-2222}}"
KEY="${2:-${NGX_BENCH_KEY:-test/bench/.key/id_ed25519}}"
USER_NAME=ngxtest
EXPECTED_FILES=130
TOLERANCE=5

SSH=(ssh
    -i "$KEY"
    -p "$PORT"
    -o IdentitiesOnly=yes
    -o BatchMode=yes
    -o StrictHostKeyChecking=no
    -o UserKnownHostsFile=/dev/null
    -o LogLevel=ERROR
    -o ConnectTimeout=5
    "$USER_NAME@127.0.0.1")

failures=0
ok()   { printf 'ok    %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; failures=$((failures + 1)); }

if [ ! -f "$KEY" ]; then
    echo "smoke: key $KEY does not exist. Run 'make bench-up'." >&2
    exit 1
fi

dump=$(mktemp)
trap 'rm -f "$dump"' EXIT

# --- 1. login as a NON-root user, with the generated key ------------------
identity=$("${SSH[@]}" 'printf "%s:%s" "$(id -u)" "$(id -un)"' 2>/dev/null)
uid="${identity%%:*}"
name="${identity##*:}"
if [ -n "$identity" ] && [ "$name" = "$USER_NAME" ] && [ "$uid" != "0" ]; then
    ok "1. ssh gets in with the generated key as $name (uid $uid, non-root)"
else
    fail "1. ssh did not get in as a non-root user (got: '${identity:-<empty>}')"
    echo "smoke: with no ssh session there is nothing to check" >&2
    exit 1
fi

# --- 2. nginx -T requires privilege, and the target does not escalate ------
output_without_sudo=$("${SSH[@]}" 'nginx -T' 2>&1)
code_without_sudo=$?
if [ "$code_without_sudo" -ne 0 ]; then
    ok "2a. 'nginx -T' fails for $USER_NAME (exit $code_without_sudo: ${output_without_sudo%%$'\n'*})"
else
    fail "2a. 'nginx -T' worked without privilege; the DR5 trap does not exist"
fi

# `nginx -T` fails first on the error log; what really matters is that the
# configuration is unreadable to the ordinary user, otherwise 2a would pass
# for the wrong reason and an `ngx` reading over SFTP would still see
# everything.
if "${SSH[@]}" 'cat /etc/nginx/nginx.conf' >/dev/null 2>&1; then
    fail "2a'. /etc/nginx/nginx.conf is readable by $USER_NAME"
else
    ok "2a'. /etc/nginx/nginx.conf is unreadable by $USER_NAME (an sftp read is barred too)"
fi

"${SSH[@]}" 'sudo -n nginx -T' > "$dump" 2>/dev/null
code_with_sudo=$?
if [ "$code_with_sudo" -eq 0 ] && [ -s "$dump" ]; then
    ok "2b. 'sudo -n nginx -T' works with no password and returns the effective config"
else
    fail "2b. 'sudo -n nginx -T' failed (exit $code_with_sudo)"
fi

# --- 3. ~130 files and the three wildcards resolving in the container -----
n_files=$(grep -c '^# configuration file ' "$dump")
if [ "$n_files" -ge $((EXPECTED_FILES - TOLERANCE)) ] && \
   [ "$n_files" -le $((EXPECTED_FILES + TOLERANCE)) ]; then
    ok "3a. effective configuration has $n_files files (~$EXPECTED_FILES)"
else
    fail "3a. effective configuration has $n_files files, expected ~$EXPECTED_FILES"
fi

check_wildcard() {
    local pattern="$1" prefix="$2"
    local n
    if ! grep -qF "include $pattern;" "$dump"; then
        fail "3b. the directive 'include $pattern;' is not in the configuration"
        return
    fi
    n=$(grep -c "^# configuration file $prefix" "$dump")
    if [ "$n" -ge 1 ]; then
        ok "3b. '$pattern' resolves inside the container ($n file(s))"
    else
        fail "3b. '$pattern' resolved no file at all"
    fi
}
check_wildcard '/usr/share/nginx/modules/*.conf' '/usr/share/nginx/modules/'
check_wildcard '/etc/nginx/conf.d/*.conf'        '/etc/nginx/conf.d/'
check_wildcard '/etc/nginx/default.d/*.conf'     '/etc/nginx/default.d/'

if grep -q 'LOCAL-TRAP-MUST-NOT-APPEAR' "$dump"; then
    fail "3c. the local trap leaked into the container's configuration"
else
    ok "3c. the local trap's marker does not appear in the container's config"
fi

# --- 4. a secret in the configuration, to exercise redaction --------------
missing=()
grep -q 'Bearer ngx-bench-token-' "$dump" || missing+=('literal token')
grep -q 'auth_basic_user_file /etc/nginx/secrets/htpasswd' "$dump" || missing+=('auth_basic_user_file')
grep -q 'ssl_certificate_key /etc/nginx/secrets/tls.key' "$dump" || missing+=('ssl_certificate_key')
if [ "${#missing[@]}" -eq 0 ]; then
    ok "4. there is a secret in the config: literal token, auth_basic_user_file and private key"
else
    fail "4. secret missing from the config: ${missing[*]}"
fi

# --- extra: SFTP, which is how ngx reads the remote config ----------------
if printf 'pwd\nquit\n' | sftp -q -i "$KEY" -P "$PORT" \
        -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
        "$USER_NAME@127.0.0.1" >/dev/null 2>&1; then
    ok "5. the sftp subsystem answers (the remote config read path)"
else
    fail "5. the sftp subsystem does not answer"
fi

echo
if [ "$failures" -eq 0 ]; then
    echo "smoke: bench OK"
    exit 0
fi
echo "smoke: $failures property/properties not proved"
exit 1
