#!/usr/bin/env bash
# Prova, uma a uma, as propriedades que o oraculo de Lua tem que ter.
# Nao depende do binario ngx: valida o ALVO, nao o cliente.
#
# Uso: test/bench/smoke-lua.sh [nome-do-container]
set -uo pipefail

CT="${1:-${NGX_BENCH_LUA_CT:-ngx-bench-lua}}"
FIXTURE="$(cd "$(dirname "$0")" && pwd)/testdata/lua_surface.conf"

failures=0
ok()   { printf 'ok    %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*"; failures=$((failures + 1)); }

if ! docker inspect "$CT" >/dev/null 2>&1; then
    echo "smoke-lua: container $CT nao existe. Rode 'make bench-lua-up'." >&2
    exit 1
fi

# Roda `openresty -t` sobre o que vier no stdin. Escreve em /tmp dentro do
# container: o arquivo nunca sai da bancada.
test_conf() {
    docker exec -i "$CT" sh -c 'cat > /tmp/smoke.conf' || return 1
    docker exec "$CT" openresty -t -c /tmp/smoke.conf 2>&1
}

# --- 1. o modulo que da sentido a imagem esta compilado -------------------
if docker exec "$CT" openresty -V 2>&1 | grep -q -- '--add-module=../ngx_lua'; then
    versao=$(docker exec "$CT" openresty -v 2>&1)
    ok "1. lua-nginx-module compilado ($versao)"
else
    fail "1. o binario nao tem lua-nginx-module; a imagem nao serve de oraculo"
fi

# --- 2. a diretiva que a bancada principal recusa e aceita aqui -----------
saida=$(printf 'events { worker_connections 16; }\nhttp { server { listen 8080; location / { content_by_lua_block { ngx.say("ok") } } } }\n' | test_conf)
if grep -q 'syntax is ok' <<<"$saida"; then
    ok "2. content_by_lua_block e aceito (o que o nginx 1.20.1 da bancada recusa)"
else
    fail "2. content_by_lua_block foi recusado: ${saida%%$'\n'*}"
fi

# --- 3. a fixture de superficie Lua passa --------------------------------
if [ ! -f "$FIXTURE" ]; then
    fail "3. fixture $FIXTURE nao existe"
else
    saida=$(test_conf < "$FIXTURE")
    if grep -q 'syntax is ok' <<<"$saida"; then
        ok "3. testdata/lua_surface.conf e aceita pelo OpenResty real"
    else
        fail "3. o OpenResty recusou a fixture: $saida"
    fi
fi

# --- 4. o oraculo sabe dizer NAO -----------------------------------------
# Sem isto, um `openresty -t` que aceitasse qualquer coisa passaria os testes
# acima e nao provaria nada.
#
# O NAO tem que ser sobre a DELIMITACAO do bloco, e nao sobre o Lua de dentro:
# medido nesta imagem, `openresty -t` nao compila o corpo -- `content_by_lua_block
# { if end }` e ate `{ this is not lua !!! }` passam. O modulo so lexa o corpo
# para achar onde ele termina. E exatamente essa a pergunta que o ngx tambem
# responde, entao o oraculo cobre o que precisa cobrir -- e nada alem disso.
saida=$(printf 'events { worker_connections 16; }\nhttp { server { listen 8080; location / { content_by_lua_block { ngx.say("a")\n' | test_conf)
if grep -q 'syntax is ok' <<<"$saida"; then
    fail "4. o oraculo aceitou um bloco Lua sem fim; ele nao delimita nada"
else
    ok "4. o oraculo recusa bloco Lua nao terminado (${saida##*: })"
fi

# --- 5. a propriedade que o caminho Lua inteiro existe para preservar -----
# Um `}` dentro de string Lua NAO fecha o bloco. E o que separa "lexar Lua" de
# "ler chaves", e o que o ngx tem que reproduzir token a token.
saida=$(printf 'events { worker_connections 16; }\nhttp { server { listen 8080; location / { content_by_lua_block { local s = "} ; {" ngx.say(s) } access_log off; } } }\n' | test_conf)
if grep -q 'syntax is ok' <<<"$saida"; then
    ok "5. '}' dentro de string Lua nao fecha o bloco, e a diretiva seguinte e vista"
else
    fail "5. o oraculo se perdeu com '}' dentro de string: ${saida%%$'\n'*}"
fi


echo
if [ "$failures" -eq 0 ]; then
    echo "smoke-lua: oraculo OK"
    exit 0
fi
echo "smoke-lua: $failures propriedade(s) nao provada(s)"
exit 1
