#!/bin/sh
# Generates the bench configuration inside the image.
#
# Reproduces the shape measured in production (Oracle Linux 9 / nginx 1.20.1):
#   - three wildcard patterns that have to resolve INSIDE the target:
#       include /usr/share/nginx/modules/*.conf;   (top level)
#       include /etc/nginx/conf.d/*.conf;          (http)
#       include /etc/nginx/default.d/*.conf;       (server)
#   - on the order of 130 files in the effective configuration;
#   - a secret inside the configuration, to exercise redaction;
#   - everything readable by root only, to exercise the privilege requirement.
#
# Usage: generate-config.sh [total-files]
set -eu

TOTAL="${1:-130}"

ETC=/etc/nginx
MODULES=/usr/share/nginx/modules
SECRETS="$ETC/secrets"

# How many files each part contributes to the `nginx -T` dump.
# Fixed: nginx.conf itself and the mime.types included in the http block.
FIXED=2
# The modules come from the nginx-mod-* packages; we count what actually
# exists instead of guessing, because the number changes if a package comes
# in or goes out.
N_MODULES=$(find "$MODULES" -maxdepth 1 -name '*.conf' | wc -l | tr -d ' ')
N_DEFAULT_D=12
# conf.d absorbs the rest: it is the big directory in any real installation.
N_CONF_D=$((TOTAL - FIXED - N_MODULES - N_DEFAULT_D))
# Three conf.d files are written by hand (default, private, tls); the rest is
# the series.
N_SITES=$((N_CONF_D - 3))

if [ "$N_MODULES" -lt 1 ]; then
    echo "bench: no $MODULES/*.conf; the third wildcard would not resolve" >&2
    exit 1
fi
if [ "$N_SITES" -lt 1 ]; then
    echo "bench: total $TOTAL is too small for the required shape" >&2
    exit 1
fi

# The shape of the bench was measured on an nginx 1.20.1; if the image brings
# another family, the directory layout may have changed and the test starts
# lying.
VERSION=$(nginx -v 2>&1 | sed 's|.*/||')
case "$VERSION" in
    1.20.*) ;;
    *) echo "bench: expected nginx 1.20.x, found $VERSION" >&2; exit 1 ;;
esac

rm -rf "$ETC/conf.d" "$ETC/default.d" "$SECRETS"
mkdir -p "$ETC/conf.d" "$ETC/default.d" "$SECRETS"

# --- secrets -------------------------------------------------------------
# A real private key (nginx loads the pair on `nginx -T`, so a fake file would
# break the dump) and an htpasswd, both referenced by the configuration.
# Besides those, a literal token written in the .conf itself: it is the only
# one that shows up in the dumped text, and therefore the one redaction has to
# catch.
openssl req -x509 -nodes -newkey rsa:2048 -days 3650 \
    -subj '/CN=tls.bench.local' \
    -keyout "$SECRETS/tls.key" -out "$SECRETS/tls.crt" >/dev/null 2>&1
printf 'admin:%s\n' "$(openssl passwd -apr1 'bench-password')" > "$SECRETS/htpasswd"

# --- nginx.conf ----------------------------------------------------------
cat > "$ETC/nginx.conf" <<'EOF'
user nginx;
worker_processes auto;
error_log /var/log/nginx/error.log;
pid /run/nginx.pid;

# First wildcard: dynamic modules. Only resolves inside the target.
include /usr/share/nginx/modules/*.conf;

events {
    worker_connections 1024;
}

http {
    log_format main '$remote_addr - $remote_user [$time_local] "$request" '
                    '$status $body_bytes_sent "$http_referer" '
                    '"$http_user_agent" "$http_x_forwarded_for"';

    access_log /var/log/nginx/access.log main;

    sendfile on;
    tcp_nopush on;
    tcp_nodelay on;
    keepalive_timeout 65;
    types_hash_max_size 4096;
    server_names_hash_bucket_size 128;
    server_names_hash_max_size 4096;

    include /etc/nginx/mime.types;
    default_type application/octet-stream;

    # Second wildcard: the sites.
    include /etc/nginx/conf.d/*.conf;
}
EOF

# --- conf.d/00-default.conf ---------------------------------------------
cat > "$ETC/conf.d/00-default.conf" <<'EOF'
server {
    listen 8080 default_server;
    server_name _;

    root /usr/share/nginx/html;

    # Third wildcard: server snippets. Only resolves inside the target.
    include /etc/nginx/default.d/*.conf;

    location / {
        return 200 "ngx bench\n";
    }
}
EOF

# --- conf.d/05-private.conf ---------------------------------------------
cat > "$ETC/conf.d/05-private.conf" <<'EOF'
server {
    listen 8080;
    server_name private.bench.local;

    location / {
        auth_basic "bench restricted area";
        auth_basic_user_file /etc/nginx/secrets/htpasswd;

        # A literal secret in the configuration: this is the one that comes out
        # of `nginx -T` and that the remote path has to redact before handing
        # it to an agent.
        proxy_set_header Authorization "Bearer ngx-bench-token-4f3c9a1b2e";
        proxy_pass http://127.0.0.1:9000;
    }
}
EOF

# --- conf.d/06-tls.conf --------------------------------------------------
cat > "$ETC/conf.d/06-tls.conf" <<'EOF'
server {
    listen 8443 ssl;
    server_name tls.bench.local;

    ssl_certificate     /etc/nginx/secrets/tls.crt;
    ssl_certificate_key /etc/nginx/secrets/tls.key;

    location / {
        return 200 "bench tls\n";
    }
}
EOF

# --- conf.d: the series --------------------------------------------------
i=1
while [ "$i" -le "$N_SITES" ]; do
    n=$(printf '%03d' "$i")
    cat > "$ETC/conf.d/site-$n.conf" <<EOF
server {
    listen 8080;
    server_name site-$n.bench.local;

    access_log /var/log/nginx/site-$n.access.log main;

    location / {
        return 200 "site-$n\n";
    }

    location /health {
        return 200 "ok\n";
    }
}
EOF
    i=$((i + 1))
done

# --- default.d -----------------------------------------------------------
i=1
while [ "$i" -le "$N_DEFAULT_D" ]; do
    n=$(printf '%02d' "$i")
    cat > "$ETC/default.d/snippet-$n.conf" <<EOF
location = /snippet-$n {
    return 200 "snippet-$n\n";
}
EOF
    i=$((i + 1))
done

# --- permissions ---------------------------------------------------------
# Configuration readable by root only: without this, `nginx -T` would work for
# the ordinary user and the bench would not prove the privilege requirement.
chown -R root:root "$ETC"
find "$ETC" -type d -exec chmod 0700 {} +
find "$ETC" -type f -exec chmod 0600 {} +

# The validation runs as root and fails the build if the generated shape is
# not valid.
nginx -t

REAL_TOTAL=$(nginx -T 2>/dev/null | grep -c '^# configuration file ')
echo "bench: $REAL_TOTAL files in the effective configuration" \
     "(modules=$N_MODULES conf.d=$N_CONF_D default.d=$N_DEFAULT_D)"
if [ "$REAL_TOTAL" -ne "$TOTAL" ]; then
    echo "bench: expected $TOTAL files, got $REAL_TOTAL" >&2
    exit 1
fi
