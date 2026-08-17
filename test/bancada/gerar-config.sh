#!/bin/sh
# Gera a configuracao da bancada dentro da imagem.
#
# Reproduz a forma medida em producao (Oracle Linux 9 / nginx 1.20.1):
#   - tres padroes com curinga que precisam resolver DENTRO do alvo:
#       include /usr/share/nginx/modules/*.conf;   (topo)
#       include /etc/nginx/conf.d/*.conf;          (http)
#       include /etc/nginx/default.d/*.conf;       (server)
#   - ordem de 130 arquivos na configuracao efetiva;
#   - segredo dentro da configuracao, para exercitar redacao;
#   - tudo legivel so por root, para exercitar a exigencia de privilegio.
#
# Uso: gerar-config.sh [total-de-arquivos]
set -eu

TOTAL="${1:-130}"

ETC=/etc/nginx
MODULES=/usr/share/nginx/modules
SEGREDOS="$ETC/secrets"

# Quantos arquivos cada parte contribui para o dump de `nginx -T`.
# Fixos: o proprio nginx.conf e o mime.types incluido no bloco http.
FIXOS=2
# Os modulos vem dos pacotes nginx-mod-*; contamos o que de fato existe em
# vez de chutar, porque o numero muda se um pacote entrar ou sair.
N_MODULOS=$(find "$MODULES" -maxdepth 1 -name '*.conf' | wc -l | tr -d ' ')
N_DEFAULT_D=12
# conf.d absorve o resto: e o diretorio grande em qualquer instalacao real.
N_CONF_D=$((TOTAL - FIXOS - N_MODULOS - N_DEFAULT_D))
# Tres arquivos de conf.d sao escritos a mao (padrao, privado, tls);
# o restante e serie.
N_SITES=$((N_CONF_D - 3))

if [ "$N_MODULOS" -lt 1 ]; then
    echo "bancada: nenhum $MODULES/*.conf; o terceiro curinga nao resolveria" >&2
    exit 1
fi
if [ "$N_SITES" -lt 1 ]; then
    echo "bancada: total $TOTAL pequeno demais para a forma exigida" >&2
    exit 1
fi

# A forma da bancada foi medida num nginx 1.20.1; se a imagem trouxer outra
# familia, o layout de diretorios pode ter mudado e o teste passa a mentir.
VERSAO=$(nginx -v 2>&1 | sed 's|.*/||')
case "$VERSAO" in
    1.20.*) ;;
    *) echo "bancada: esperado nginx 1.20.x, encontrado $VERSAO" >&2; exit 1 ;;
esac

rm -rf "$ETC/conf.d" "$ETC/default.d" "$SEGREDOS"
mkdir -p "$ETC/conf.d" "$ETC/default.d" "$SEGREDOS"

# --- segredos ------------------------------------------------------------
# Chave privada de verdade (o nginx carrega o par no `nginx -T`, entao um
# arquivo falso quebraria o dump) e um htpasswd, os dois referenciados pela
# configuracao. Alem deles, um token literal escrito no proprio .conf: e o
# unico que aparece no texto dumpado, e portanto o que a redacao precisa pegar.
openssl req -x509 -nodes -newkey rsa:2048 -days 3650 \
    -subj '/CN=tls.bancada.local' \
    -keyout "$SEGREDOS/tls.key" -out "$SEGREDOS/tls.crt" >/dev/null 2>&1
printf 'admin:%s\n' "$(openssl passwd -apr1 'senha-da-bancada')" > "$SEGREDOS/htpasswd"

# --- nginx.conf ----------------------------------------------------------
cat > "$ETC/nginx.conf" <<'EOF'
user nginx;
worker_processes auto;
error_log /var/log/nginx/error.log;
pid /run/nginx.pid;

# Primeiro curinga: modulos dinamicos. So resolve dentro do alvo.
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

    # Segundo curinga: os sites.
    include /etc/nginx/conf.d/*.conf;
}
EOF

# --- conf.d/00-default.conf ---------------------------------------------
cat > "$ETC/conf.d/00-default.conf" <<'EOF'
server {
    listen 8080 default_server;
    server_name _;

    root /usr/share/nginx/html;

    # Terceiro curinga: trechos de server. So resolve dentro do alvo.
    include /etc/nginx/default.d/*.conf;

    location / {
        return 200 "bancada ngx\n";
    }
}
EOF

# --- conf.d/05-privado.conf ---------------------------------------------
cat > "$ETC/conf.d/05-privado.conf" <<'EOF'
server {
    listen 8080;
    server_name privado.bancada.local;

    location / {
        auth_basic "area restrita da bancada";
        auth_basic_user_file /etc/nginx/secrets/htpasswd;

        # Segredo literal na configuracao: e este que sai no `nginx -T` e que
        # o caminho remoto tem de redigir antes de entregar a um agente.
        proxy_set_header Authorization "Bearer ngx-bancada-token-4f3c9a1b2e";
        proxy_pass http://127.0.0.1:9000;
    }
}
EOF

# --- conf.d/06-tls.conf --------------------------------------------------
cat > "$ETC/conf.d/06-tls.conf" <<'EOF'
server {
    listen 8443 ssl;
    server_name tls.bancada.local;

    ssl_certificate     /etc/nginx/secrets/tls.crt;
    ssl_certificate_key /etc/nginx/secrets/tls.key;

    location / {
        return 200 "tls da bancada\n";
    }
}
EOF

# --- conf.d: a serie -----------------------------------------------------
i=1
while [ "$i" -le "$N_SITES" ]; do
    n=$(printf '%03d' "$i")
    cat > "$ETC/conf.d/site-$n.conf" <<EOF
server {
    listen 8080;
    server_name site-$n.bancada.local;

    access_log /var/log/nginx/site-$n.access.log main;

    location / {
        return 200 "site-$n\n";
    }

    location /saude {
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
    cat > "$ETC/default.d/trecho-$n.conf" <<EOF
location = /trecho-$n {
    return 200 "trecho-$n\n";
}
EOF
    i=$((i + 1))
done

# --- permissoes ----------------------------------------------------------
# Configuracao legivel so por root: sem isso, `nginx -T` funcionaria para o
# usuario comum e a bancada nao provaria a exigencia de privilegio.
chown -R root:root "$ETC"
find "$ETC" -type d -exec chmod 0700 {} +
find "$ETC" -type f -exec chmod 0600 {} +

# A validacao roda como root e falha o build se a forma gerada nao for valida.
nginx -t

TOTAL_REAL=$(nginx -T 2>/dev/null | grep -c '^# configuration file ')
echo "bancada: $TOTAL_REAL arquivos na configuracao efetiva" \
     "(modules=$N_MODULOS conf.d=$N_CONF_D default.d=$N_DEFAULT_D)"
if [ "$TOTAL_REAL" -ne "$TOTAL" ]; then
    echo "bancada: esperado $TOTAL arquivos, obtido $TOTAL_REAL" >&2
    exit 1
fi
