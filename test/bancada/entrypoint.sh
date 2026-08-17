#!/bin/sh
# Sobe a bancada: instala a chave publica de teste, gera chaves de host novas,
# liga o nginx e entrega o processo ao sshd.
set -eu

CHAVE_PUB=/chave-publica.pub

if [ ! -f "$CHAVE_PUB" ]; then
    echo "bancada: falta $CHAVE_PUB no container. Suba com 'make bancada-up'," \
         "que gera a chave no host e a copia para ca." >&2
    exit 1
fi

install -d -m 0700 -o ngxtest -g ngxtest /home/ngxtest/.ssh
cat "$CHAVE_PUB" > /home/ngxtest/.ssh/authorized_keys
chown ngxtest:ngxtest /home/ngxtest/.ssh/authorized_keys
chmod 0600 /home/ngxtest/.ssh/authorized_keys

# Chaves de host novas a cada subida: o teste de recusa por host key
# desconhecida depende de o container nao reaproveitar chave da subida
# anterior, que ja estaria no known_hosts de quem rodou antes.
ssh-keygen -A >/dev/null

nginx -g 'daemon on;'

exec /usr/sbin/sshd -D -e
