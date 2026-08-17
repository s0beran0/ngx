#!/usr/bin/env bash
# Prova, uma a uma, as propriedades que a bancada precisa ter.
# Nao depende do binario do ngx: valida o ALVO, nao o cliente.
#
# Uso: test/bancada/smoke.sh [porta] [caminho-da-chave]
set -uo pipefail

PORTA="${1:-${NGX_BANCADA_PORTA:-2222}}"
CHAVE="${2:-${NGX_BANCADA_CHAVE:-test/bancada/.chave/id_ed25519}}"
USUARIO=ngxtest
ESPERADO_ARQUIVOS=130
TOLERANCIA=5

SSH=(ssh
    -i "$CHAVE"
    -p "$PORTA"
    -o IdentitiesOnly=yes
    -o BatchMode=yes
    -o StrictHostKeyChecking=no
    -o UserKnownHostsFile=/dev/null
    -o LogLevel=ERROR
    -o ConnectTimeout=5
    "$USUARIO@127.0.0.1")

falhas=0
ok()   { printf 'ok    %s\n' "$*"; }
falha() { printf 'FALHA %s\n' "$*"; falhas=$((falhas + 1)); }

if [ ! -f "$CHAVE" ]; then
    echo "smoke: chave $CHAVE nao existe. Rode 'make bancada-up'." >&2
    exit 1
fi

dump=$(mktemp)
trap 'rm -f "$dump"' EXIT

# --- 1. login como usuario NAO-root, com a chave gerada -------------------
identidade=$("${SSH[@]}" 'printf "%s:%s" "$(id -u)" "$(id -un)"' 2>/dev/null)
uid="${identidade%%:*}"
nome="${identidade##*:}"
if [ -n "$identidade" ] && [ "$nome" = "$USUARIO" ] && [ "$uid" != "0" ]; then
    ok "1. ssh entra com a chave gerada como $nome (uid $uid, nao-root)"
else
    falha "1. ssh nao entrou como usuario nao-root (obtido: '${identidade:-<vazio>}')"
    echo "smoke: sem sessao ssh nao ha o que verificar" >&2
    exit 1
fi

# --- 2. nginx -T exige privilegio, e o alvo nao escala sozinho ------------
saida_sem_sudo=$("${SSH[@]}" 'nginx -T' 2>&1)
codigo_sem_sudo=$?
if [ "$codigo_sem_sudo" -ne 0 ]; then
    ok "2a. 'nginx -T' falha para $USUARIO (saida $codigo_sem_sudo: ${saida_sem_sudo%%$'\n'*})"
else
    falha "2a. 'nginx -T' funcionou sem privilegio; a armadilha da DR5 nao existe"
fi

# O `nginx -T` falha primeiro no log de erro; o que interessa mesmo e que a
# configuracao seja ilegivel para o usuario comum, senao 2a passaria por
# motivo errado e um `ngx` que lesse por SFTP ainda enxergaria tudo.
if "${SSH[@]}" 'cat /etc/nginx/nginx.conf' >/dev/null 2>&1; then
    falha "2a'. /etc/nginx/nginx.conf e legivel por $USUARIO"
else
    ok "2a'. /etc/nginx/nginx.conf e ilegivel por $USUARIO (leitura por sftp tambem barra)"
fi

"${SSH[@]}" 'sudo -n nginx -T' > "$dump" 2>/dev/null
codigo_com_sudo=$?
if [ "$codigo_com_sudo" -eq 0 ] && [ -s "$dump" ]; then
    ok "2b. 'sudo -n nginx -T' funciona sem senha e devolve a config efetiva"
else
    falha "2b. 'sudo -n nginx -T' falhou (saida $codigo_com_sudo)"
fi

# --- 3. ~130 arquivos e os tres curingas resolvendo no container ----------
n_arquivos=$(grep -c '^# configuration file ' "$dump")
if [ "$n_arquivos" -ge $((ESPERADO_ARQUIVOS - TOLERANCIA)) ] && \
   [ "$n_arquivos" -le $((ESPERADO_ARQUIVOS + TOLERANCIA)) ]; then
    ok "3a. configuracao efetiva tem $n_arquivos arquivos (~$ESPERADO_ARQUIVOS)"
else
    falha "3a. configuracao efetiva tem $n_arquivos arquivos, esperado ~$ESPERADO_ARQUIVOS"
fi

verificar_curinga() {
    local padrao="$1" prefixo="$2"
    local n
    if ! grep -qF "include $padrao;" "$dump"; then
        falha "3b. a diretiva 'include $padrao;' nao esta na configuracao"
        return
    fi
    n=$(grep -c "^# configuration file $prefixo" "$dump")
    if [ "$n" -ge 1 ]; then
        ok "3b. '$padrao' resolve dentro do container ($n arquivo(s))"
    else
        falha "3b. '$padrao' nao resolveu nenhum arquivo"
    fi
}
verificar_curinga '/usr/share/nginx/modules/*.conf' '/usr/share/nginx/modules/'
verificar_curinga '/etc/nginx/conf.d/*.conf'        '/etc/nginx/conf.d/'
verificar_curinga '/etc/nginx/default.d/*.conf'     '/etc/nginx/default.d/'

if grep -q 'ARMADILHA-LOCAL-NAO-DEVE-APARECER' "$dump"; then
    falha "3c. a armadilha local vazou para a configuracao do container"
else
    ok "3c. o marcador da armadilha local nao aparece na config do container"
fi

# --- 4. segredo na configuracao, para exercitar redacao -------------------
faltando=()
grep -q 'Bearer ngx-bancada-token-' "$dump" || faltando+=('token literal')
grep -q 'auth_basic_user_file /etc/nginx/secrets/htpasswd' "$dump" || faltando+=('auth_basic_user_file')
grep -q 'ssl_certificate_key /etc/nginx/secrets/tls.key' "$dump" || faltando+=('ssl_certificate_key')
if [ "${#faltando[@]}" -eq 0 ]; then
    ok "4. ha segredo na config: token literal, auth_basic_user_file e chave privada"
else
    falha "4. segredo ausente na config: ${faltando[*]}"
fi

# --- complementar: SFTP, que e como o ngx le a config remota --------------
if printf 'pwd\nquit\n' | sftp -q -i "$CHAVE" -P "$PORTA" \
        -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
        "$USUARIO@127.0.0.1" >/dev/null 2>&1; then
    ok "5. subsistema sftp responde (caminho de leitura da config remota)"
else
    falha "5. subsistema sftp nao responde"
fi

echo
if [ "$falhas" -eq 0 ]; then
    echo "smoke: bancada OK"
    exit 0
fi
echo "smoke: $falhas propriedade(s) nao provada(s)"
exit 1
