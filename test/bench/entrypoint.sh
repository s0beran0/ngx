#!/bin/sh
# Brings the bench up: installs the public test key, generates fresh host
# keys, starts nginx and hands the process over to sshd.
set -eu

PUBLIC_KEY=/public-key.pub

if [ ! -f "$PUBLIC_KEY" ]; then
    echo "bench: $PUBLIC_KEY is missing in the container. Bring it up with" \
         "'make bench-up', which generates the key on the host and copies it here." >&2
    exit 1
fi

install -d -m 0700 -o ngxtest -g ngxtest /home/ngxtest/.ssh
cat "$PUBLIC_KEY" > /home/ngxtest/.ssh/authorized_keys
chown ngxtest:ngxtest /home/ngxtest/.ssh/authorized_keys
chmod 0600 /home/ngxtest/.ssh/authorized_keys

# Fresh host keys on every startup: the test for refusing an unknown host key
# depends on the container not reusing a key from the previous startup, which
# would already be in the known_hosts of whoever ran it before.
ssh-keygen -A >/dev/null

nginx -g 'daemon on;'

exec /usr/sbin/sshd -D -e
