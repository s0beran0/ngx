# Bancada de teste

Container descartável com `sshd` e `nginx`, usado como alvo dos testes de
integração do caminho remoto do `ngx`. É artefato versionado do repositório:
quem clona o projeto sobe a bancada com um comando, em Linux ou macOS, sem
mais nada instalado além de Docker e `ssh`.

```sh
make bancada-up      # gera a chave, constrói a imagem e sobe o container
make bancada-smoke   # prova, uma a uma, as propriedades exigidas
make bancada-down    # derruba o container, remove a imagem e apaga a chave
```

Auxiliares: `make bancada-logs` (log do `sshd`) e `make bancada-shell` (sessão
interativa como `ngxtest`).

## Como conectar

| | |
|---|---|
| Endereço | `127.0.0.1`, porta **2222** (fixa; `make BANCADA_PORTA=2223 bancada-up` para trocar) |
| Usuário | `ngxtest`, uid 1000, **não-root** |
| Chave privada | `test/bancada/.chave/id_ed25519` (ed25519, sem passphrase) |
| Privilégio | `sudo -n /usr/sbin/nginx`, sem senha, **só** o nginx |
| nginx | 1.20.1, Oracle Linux 9 |

A porta só escuta em `127.0.0.1`. A chave é **gerada** por `make bancada-up` e
nunca entra no git (`test/bancada/.chave/` está no `.gitignore`); as chaves de
host do container são geradas a cada subida, para que o teste de recusa por
host key desconhecida não esbarre num `known_hosts` da execução anterior.

## O que a bancada reproduz

A forma foi medida num nginx de produção real (Oracle Linux 9, nginx 1.20.1) e
é o motivo de a imagem base ser `oraclelinux:9`: é dessa família que vem o
layout com três diretórios de include e o nginx compilado com `--modules-path`,
que dá o terceiro curinga sem gambiarra. O módulo `nginx` do appstream é
desabilitado no build para valer o pacote não-modular, que é justamente o
1.20.1 — sem isso, o dia em que o stream padrão virar 1.26 a bancada mudaria
sozinha. O `gerar-config.sh` aborta se a versão não for 1.20.x.

**Três padrões com curinga**, que só resolvem dentro do container:

| Diretiva | Contexto | Arquivos |
|---|---|---|
| `include /usr/share/nginx/modules/*.conf;` | topo | 4 (pacotes `nginx-mod-*`) |
| `include /etc/nginx/conf.d/*.conf;` | `http` | 112 |
| `include /etc/nginx/default.d/*.conf;` | `server` | 12 |

**130 arquivos na configuração efetiva**, contando `nginx.conf` e
`mime.types`. O número é conferido no próprio build: `gerar-config.sh` roda
`nginx -T` e falha se o total não bater. É o volume que torna a latência
sequencial visível — um alvo com três arquivos passa em tudo e não prova nada.

**`nginx -T` legível só por root.** `/etc/nginx` é `0700 root:root` e os
arquivos `0600`, então nem `nginx -T` nem uma leitura por SFTP enxergam a
configuração como `ngxtest`. O `sudo` sem senha existe, mas restrito ao
binário do nginx: é a armadilha da DR5, o alvo onde um cliente que escalasse
sozinho passaria despercebido.

**Segredo dentro da configuração**, em três formas, para exercitar a redação
ponta a ponta:

- token literal no texto — `proxy_set_header Authorization "Bearer ngx-bancada-token-4f3c9a1b2e";`
  em `conf.d/05-privado.conf`. É o único que aparece no dump do `nginx -T`, e
  portanto o que a redação tem de pegar;
- `auth_basic_user_file /etc/nginx/secrets/htpasswd;`
- `ssl_certificate_key /etc/nginx/secrets/tls.key;` — chave RSA de verdade,
  gerada no build, porque o `nginx -T` carrega o par e um arquivo falso
  quebraria o dump.

Nenhum desses segredos é real nem sai do container.

## A armadilha do glob

`armadilha-local/etc/nginx/conf.d/zz-armadilha-local.conf` é um arquivo com o
mesmo nome de diretório usado dentro do container, mas **no disco local**. Ele
só entra numa árvore lida da bancada se o `Glob` do parser não estiver
injetado com o sistema de arquivos remoto — o defeito que a Task R3 corrigiu.

O marcador `ARMADILHA-LOCAL-NAO-DEVE-APARECER` nunca pode surgir numa
configuração efetiva lida do container; o smoke test verifica isso, e o teste
de integração em Go deve apontar seu sistema de arquivos local para esse
diretório ao exercitar o caso.

## Arquivos

| Arquivo | Papel |
|---|---|
| `Dockerfile` | imagem: Oracle Linux 9, nginx 1.20.1, `sshd`, `sudo`, usuário `ngxtest` |
| `sshd_config.bancada` | `sshd` só por chave; `PermitRootLogin no`, sem senha, com SFTP |
| `gerar-config.sh` | gera os 130 arquivos no build e confere o total |
| `entrypoint.sh` | instala a chave pública, gera chaves de host, liga nginx e `sshd` |
| `smoke.sh` | prova as propriedades acima contra o container no ar |
| `armadilha-local/` | homônimo local para o teste do glob |

O container é descartável e roda só local: não tem hardening, e não deveria
ser exposto. O que ele não tem, de propósito: senha de root, login de root e
autenticação por senha.
