# ngx

CLI em Go que torna o nginx operavel por programa: saida JSON estruturada,
leitura por seletor, e — quando a v0.2 chegar — mudancas transacionais com
rollback.

## Dois publicos, uma ferramenta

O `ngx` e feito para ser usado por **agentes de IA** e por **humanos**, e a
saida se adapta sozinha: quando a stdout **nao** e um terminal, ela e JSON;
quando e, ela e legivel. `--json` e `--human` forcam um dos dois.

Isso nao e enfeite. Um agente que le um pipe precisa de estrutura para
decidir; uma pessoa depurando precisa de texto. A mesma invocacao serve aos
dois sem ninguem ter que lembrar de uma flag.

Onde o comportamento diverge, a divergencia e regra de seguranca. O
`--no-redact`, que desliga a ocultacao de valores sensiveis, so e aceito em
terminal:

```console
$ ngx --no-redact inspect -c nginx.conf | cat
{"ok":false,"command":"inspect","ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0002","message":"--no-redact so e aceito quando a saida e um terminal"}],"meta":{"duration_ms":0,"target":"local"}}
```

Um humano que pede para ver o segredo o ve na tela. Um agente lendo o pipe,
estruturalmente, nem consegue pedir.

## Estado atual — leia antes de tentar instalar

Esta e a **v0.1, em desenvolvimento**, e ela e **somente leitura**. Nada aqui
altera a configuracao do nginx.

- **Nao ha release publicada.** O repositorio nao tem nenhuma tag ainda. Os
  instaladores `install.sh` e `install.ps1` estao no repositorio e ja
  funcionam do ponto de vista do codigo, mas nao ha o que eles baixem — e a
  chave publica minisign do projeto ainda nao foi gerada, entao o `install.sh`
  recusa instalar de proposito, porque ausencia de verificacao e falha, nunca
  um "segui em frente". **A unica forma de obter o `ngx` hoje e compilar da
  fonte.**
- **Existem dois comandos:** `version` e `inspect`. Comandos previstos no
  desenho (`get`, `tree`, `fmt`, `test`, `diff`, `apply`, `update`) ainda nao
  existem — `ngx update` sai com "unknown command", mesmo havendo codigo de
  atualizacao no repositorio, porque o comando nao esta registrado.
- **O acesso remoto por SSH existe e funciona**, mas nao foi exercitado contra
  um servidor de producao por este projeto. Ver [`docs/remoto.md`](docs/remoto.md).
- **A saida "humana" ainda e crua:** hoje ela e o JSON dos dados formatado com
  indentacao, e o erro como uma linha de texto. Melhorar isso e trabalho
  pendente, nao um estilo deliberado.

## Compilando da fonte

Precisa de Go 1.25 ou mais novo. Sem CGO, sem dependencia de sistema.

```console
$ make build
CGO_ENABLED=0 go build -o bin/ngx ./cmd/ngx

$ ./bin/ngx version
{"ok":true,"command":"version","ngx_version":"0.1.0-dev","data":{"version":"0.1.0-dev"},"diagnostics":[],"meta":{"duration_ms":0,"target":"local"}}
```

Copie `bin/ngx` para onde quiser — e um binario estatico, sem instalador.

Outros alvos uteis: `make test`, `make test-race`, `make lint`, `make
verificar` (o que o CI roda) e `make ajuda` para a lista completa.

### Os instaladores, quando houver release

`install.sh` (Linux e macOS) e `install.ps1` (Windows) ja estao no
repositorio. Enquanto nao houver release publicada e chave minisign gerada,
eles nao tem o que instalar — mas o `--help` deles ja e a referencia correta
das variaveis:

```console
$ sh install.sh --help
install.sh — instalador do ngx para Linux e macOS
...
```

| Variavel | Efeito |
|---|---|
| `NGX_INSTALL_DIR` | diretorio de instalacao (`/usr/local/bin`; `%LOCALAPPDATA%\ngx\bin` no Windows) |
| `NGX_CHANNEL` | `stable` (default) ou `beta`, que inclui `-rc`/`-beta`/`-alpha` |
| `NGX_VERSION` | versao fixa, ex. `v0.2.0`; quando definida, a API do GitHub nao e consultada |
| `NGX_ALLOW_UNVERIFIED` | `1` permite instalar quando a assinatura **nao pode** ser verificada; nunca ignora assinatura invalida nem checksum divergente |

Nenhum dos dois chama `sudo` sozinho: se o diretorio exigir privilegio, o
script imprime a linha exata a rodar e para. Escalar privilegio por conta
propria dentro de algo executado via `curl | sh` e exatamente o que ninguem
deveria aceitar rodar.

O checksum SHA256 e conferido sempre e nao tem como ser desligado.

## Usando hoje

### `ngx inspect`

Le a configuracao e devolve a arvore inteira mais um resumo. O caminho vem de
`-c`/`--config`, ou de `nginx.config` no arquivo de configuracao do proprio
`ngx`.

```console
$ ./bin/ngx inspect -c internal/cli/testdata/exemplo.conf | jq -c '.data.summary'
{"files":1,"servers":1,"locations":2,"upstreams":1}
```

Cada no da arvore carrega `directive`, `args`, `file`, `line`, `column`, os
offsets de byte (`span` e `head_span`) e um `id` estavel — `h.s0.l0` e a
primeira `location` do primeiro `server` dentro do `http`. O envelope traz
ainda `meta.config_hash`, o hash canonico da configuracao lida.

Valores sensiveis saem redigidos por padrao:

```console
$ ./bin/ngx inspect -c internal/cli/testdata/exemplo.conf | jq -c '.data.config[0].parsed[1].block[0].block[2]'
{"directive":"ssl_certificate_key","args":["***"],"file":"internal/cli/testdata/exemplo.conf","line":7,"column":9,"span":{"start":100,"end":145},"head_span":{"start":100,"end":144},"id":"h.s0.d2"}
```

`--combine` resolve os `include` numa arvore unica em vez de uma lista de
arquivos.

### `ngx version`

```console
$ ./bin/ngx version | jq -c .data
{"version":"0.1.0-dev"}
```

### Erros

Erro de sintaxe na configuracao aponta arquivo e linha, e sai com codigo 3:

```console
$ ./bin/ngx inspect -c internal/cli/testdata/invalido.conf; echo "exit=$?"
{"ok":false,"command":"inspect","ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0003","message":"internal/cli/testdata/invalido.conf:5: unexpected end of file, expecting \"}\" in internal/cli/testdata/invalido.conf:5","file":"internal/cli/testdata/invalido.conf","line":5}],"meta":{"duration_ms":0,"target":"local"}}
exit=3
```

| Codigo | Significado |
|---|---|
| 0 | sucesso |
| 1 | falha interna ou de ambiente |
| 2 | erro de uso (flag ou comando invalido) |
| 3 | configuracao do nginx invalida |

Os codigos 7 (drift) e 9 (hash divergente) estao reservados para os comandos
de mutacao da v0.2.

### Flags globais

```
-c, --config          configuracao principal do nginx
    --json/--human    forca o formato da saida
-q, --quiet           so erros
    --no-color        desliga cores
    --nginx-bin       caminho do binario do nginx
    --nginx-version   assume esta versao do nginx
    --timeout         timeout das operacoes (default 30s)
    --profile         perfil do arquivo de configuracao do ngx
    --no-redact       mostra valores sensiveis (so em terminal)
```

As flags de acesso remoto (`--host`, `--user`, `--port`, `--key`,
`--known-hosts`, `--insecure-host-key`, `--sudo`) estao documentadas em
[`docs/remoto.md`](docs/remoto.md).

## Configuracao do proprio ngx

`/etc/ngx/ngx.yaml` (global) e `.ngx/config.yaml` (local, relativo ao
diretorio de trabalho). O local sobrescreve o global, chave a chave:

```yaml
nginx:
  binary: /usr/sbin/nginx
  config: /etc/nginx/nginx.conf
output:
  format: auto      # auto, json ou human
  redact:
    - ssl_certificate_key
```

A lista de redacao ja vem preenchida com um conjunto padrao; declara-la aqui
substitui esse conjunto.

## Acesso remoto

O `ngx` opera um host remoto por SSH sem instalar nada no servidor:

```console
$ ngx --host web1 inspect
```

Autenticacao, verificacao de host key, privilegio e a ressalva de latencia
estao em [`docs/remoto.md`](docs/remoto.md).

## Licenca

MIT. Ver [LICENSE](LICENSE).
