# ngx — Plano de Acesso Remoto via SSH

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Administrar o nginx de um servidor remoto sem instalar nada nele — o `ngx` roda na sua máquina, lê a configuração e executa o nginx por SSH.

**Architecture:** Uma camada de transporte com duas implementações, local e SSH, atrás da mesma interface. O parse remoto reaproveita o `ParseOptions.Open` que a v0.1 já desenhou como injetável, mais um `Glob` que passa a ser injetado também. Nenhuma lógica de árvore, seletor, ID, hash ou redação muda: o remoto é apenas outra fonte de bytes e outro lugar onde comandos rodam.

**Tech Stack:** `golang.org/x/crypto/ssh`, `github.com/pkg/sftp`, e — a confirmar na Task R2 — uma forma portável de falar com o `ssh-agent`.

## Nota de terminologia

Este projeto usa a palavra "agente" em dois sentidos, e confundi-los leva a
implementar a coisa errada:

- **agente de IA** — quem *consome* a saída do `ngx`. É o que a spec quer dizer
  em "o agente age sem reparsear" e é a razão de a saída ser JSON por padrão
  quando não há terminal. O `ngx` também é usado por humanos diretamente, e é
  por isso que existe `--human`.
- **`ssh-agent`** — um programa do sistema operacional, anterior a tudo isso,
  que mantém chaves SSH destravadas em memória e assina desafios em nome de
  quem autentica. Nada a ver com IA.

Neste documento, `ssh-agent` aparece sempre escrito assim, com o prefixo. Onde
estiver só "agente", trata-se do consumidor da saída.

**Spec:** `docs/superpowers/specs/2026-08-17-ngx-cli-design.md`. Este plano realiza parte do item "multi-host via SSH" que a spec coloca na v1.0 (§16), antecipado a pedido.

**Pré-requisito:** Plano 1 concluído. Este plano depende de `config.Parse`, `config.Combine`, `internal/runtime` e do envelope de saída.

## Global Constraints

- Módulo Go: `github.com/s0beran0/ngx`. Go 1.25.
- **Zero CGO.** Toda dependência nova precisa ser Go puro — verifique o `go.mod` dela antes de adicionar, e confirme com `CGO_ENABLED=0 go build` para as seis plataformas.
- **Funciona em Linux, macOS e Windows.** Nada específico de um sistema sem o caminho equivalente nos outros dois. Isso vale em particular para o `ssh-agent`, que no Windows não é um socket Unix.
- Nenhum `exec` de shell: comandos remotos são montados com argumentos explícitos e escapados, nunca concatenados numa string de shell.
- **Segredo nunca vai em flag.** Senha e passphrase vêm de prompt ou de variável de ambiente. Flag aparece em `ps`, no histórico do shell e nos logs de CI.
- Comentários de código em português, sem acentuação.
- **Mensagens de commit nunca mencionam Claude ou IA.** Sem trailer `Co-Authored-By`.

## Decisões

### DR1 — Verificação estrita de host key, com escape explícito

O `ngx` usa o `known_hosts` do usuário e **recusa** host desconhecido ou cuja chave mudou, como o `ssh` faz. Quem precisar contornar passa `--insecure-host-key`, que é verboso de propósito e fica visível no comando.

*Por quê:* o `ngx` remoto transmite a configuração do servidor e executa comandos privilegiados nele. Um cliente que aceite qualquer host key permite que qualquer máquina na rota se passe pelo servidor, capturando credencial e recebendo os comandos. Aceitar chave desconhecida em silêncio é o comportamento que faz a criptografia do SSH não servir para nada.

*Custo aceito:* o primeiro acesso a um host novo exige que ele esteja no `known_hosts` — o que se resolve com um `ssh` manual antes, e é o mesmo atrito que o `ssh` já impõe.

### DR2 — `ssh-agent` primeiro, `~/.ssh/config` depois, flags por cima

A ordem de resolução é: flags explícitas vencem; o que faltar vem do `~/.ssh/config` para aquele host; a autenticação tenta o `ssh-agent` antes de qualquer arquivo de chave.

*Por quê:* com o `ssh-agent`, a chave privada nunca é lida pelo `ngx` — ele envia o desafio e recebe a assinatura. Menos código nosso tocando material de chave é menos superfície para errar. E ler o `~/.ssh/config` significa que `ngx --host web1 inspect` funciona para quem já tem `ssh web1` funcionando, sem reconfigurar nada.

### DR3 — Nada é instalado no servidor remoto

O `ngx` não copia binário para o destino, nem temporariamente. Ele lê arquivos por SFTP e executa o `nginx` que já existe lá.

*Por quê:* é o requisito. Escrever executável em `/tmp` de um servidor de produção é o tipo de coisa que dispara alerta de EDR e que um operador tem razão em não querer.

*Custo aceito:* mais viagens de rede. Uma leitura SFTP por arquivo da configuração efetiva.

*Medido num nginx de produção real* (Oracle Linux 9, nginx 1.20.1, acesso por VPN): a configuração efetiva tem **132 arquivos** e 9.822 linhas. A estimativa original deste plano — "trinta `include`" — errou por mais de quatro vezes. Com 132 viagens sequenciais, a latência da VPN domina o tempo de resposta e uma leitura interativa deixa de ser interativa.

*Consequência:* paralelizar as leituras sai de "conserto se virar problema" e passa a ser requisito de projeto da R4. Serialize apenas o que a dependência exige: o `include` só é conhecido depois de ler quem o declara, então o paralelismo é por nível da árvore, não sobre a lista inteira.

### DR7 — `~/.ssh/config` ilegível degrada com aviso, nunca aborta

A biblioteca de parse honra `Host` (com wildcard e negação), `Include`, `Match all` e `Match Host`. Qualquer outro critério — `user`, `final`, `canonical`, `exec` — faz o parse do **arquivo inteiro** falhar, não apenas daquela entrada. Detalhes e fontes em `docs/superpowers/specs/2026-08-17-ngx-remoto-dependencias.md`.

Um `~/.ssh/config` com `Match user deploy` é perfeitamente válido para o `ssh` e nada raro. Se o `ngx` abortasse, ele quebraria para quem tem um arquivo legítimo, por limitação nossa.

Então: falha de parse vira **diagnóstico de severidade `warning`** no envelope, dizendo qual arquivo e qual linha o `ngx` não entendeu, e a resolução segue com o que veio de flags e dos defaults. O que **não** pode acontecer é o `ngx` ignorar o arquivo em silêncio e conectar em outro host que não o pretendido — o aviso é o que impede isso de virar surpresa.

### DR6 — O `ngx` não usa `sftp.Client.Glob`

O `Glob` do `github.com/pkg/sftp` **descarta erros de I/O por contrato**. O comentário da própria função diz: *"Glob ignores file system errors such as I/O errors reading directories. The only possible returned error is ErrBadPattern"* (`match.go:40-42`). E no caminho sem metacaractere ele é literal: `file, err := c.Lstat(pattern); if err != nil { return nil, nil }` — conexão caindo devolve nenhum resultado e nenhum erro.

O `ngx` implementa o próprio glob remoto sobre `ReadDir` + `path.Match`, propagando erro de I/O como erro.

*Por quê:* `include /etc/nginx/conf.d/*.conf` num link instável devolveria zero arquivos em silêncio, e o `ngx` apresentaria a configuração do servidor sem os 112 arquivos que ela tem — como se o servidor genuinamente não os tivesse. Uma ferramenta lida por agente de IA não pode ser confiantemente incompleta: o consumidor não tem como desconfiar.

*Nota:* o `filepath.Glob` da stdlib tem a mesma semântica, e localmente isso quase nunca importa. É a mesma premissa que o SSH inverte — falha de leitura deixa de ser rara e vira rotina. Vale também para o item parkeado da Task 7 no ledger, pela mesma razão.

### DR5 — Privilégio é explícito, nunca inferido

Medido no servidor de produção real: `nginx -T` **falha** para o usuário comum (`opc`) e só funciona via `sudo`. Não é exceção — a configuração do nginx costuma ser legível só por root, e num host de produção o `sudo` frequentemente está liberado sem senha. Ou seja: o caminho que "simplesmente funciona" é o de escalar privilégio em silêncio.

O `ngx` **não** faz isso. Se um comando remoto precisa de privilégio, ele só roda com `--sudo` explícito no comando; sem a flag, o `ngx` reporta que o comando exige privilégio e qual é — não tenta de novo com `sudo`, não adivinha.

*Por quê:* uma ferramenta feita para ser dirigida por agente de IA que escala privilégio sozinha, num servidor de produção, transforma um erro de leitura em um comando `root`. O atrito de digitar `--sudo` é o registro de que alguém decidiu. E como o `ngx` já tem envelope estruturado, "precisa de privilégio" é um diagnóstico acionável, não um beco sem saída.

*Consequência para a R4:* a detecção de estado precisa distinguir "não consegui ler" de "não existe", e nunca degradar em silêncio. Campo indisponível é omitido — a regra da spec já cobre isso.

### DR4 — O `Glob` do crossplane passa a ser injetado

Hoje o `ngx` injeta `Open` mas não `Glob`, então o crossplane resolve `include conf.d/*.conf` com `filepath.Glob` sobre o disco **local**. Isso está registrado como limitação conhecida da v0.1 e aqui vira defeito: apontado para um host remoto, o `ngx` listaria arquivos da máquina do operador e os trataria como configuração do servidor.

*Consequência:* a Task R3 é obrigatória e bloqueia as demais. Sem ela, o remoto mente.

---

### Task R1: Camada de transporte

**Files:**
- Create: `internal/transport/transport.go`, `internal/transport/local.go`
- Test: `internal/transport/local_test.go`

**Interfaces:**
- Consumes: nada de tarefas anteriores
- Produces: a interface `transport.Transport` com `Open(path string) (io.ReadCloser, error)`, `Glob(pattern string) ([]string, error)`, `Run(ctx context.Context, argv []string) (stdout, stderr []byte, exitCode int, err error)`, `Close() error`, e `Describe() string`; a implementação `transport.Local()`

- [ ] **Step 1: Escrever o teste**

`internal/transport/local_test.go` cobre: `Open` de arquivo existente e de inexistente; `Glob` casando e não casando; `Run` de um comando que sai com zero e de outro que sai com código diferente, verificando que `exitCode` é reportado **e** que `err` é nil nesse caso — código de saída diferente de zero é resultado, não erro de transporte. Um erro de transporte é o binário não existir ou a conexão cair.

Essa distinção é o ponto central do teste: confundir as duas coisas faz um `nginx -t` que reprova a configuração parecer falha de infraestrutura.

- [ ] **Step 2: Definir a interface e implementar o local**

`Local()` é um envelope fino sobre `os.Open`, `filepath.Glob` e `exec.CommandContext`. `Describe()` devolve algo como `"local"`, para aparecer no `meta` do envelope e quem consome a saída saber contra o que operou.

- [ ] **Step 3: Rodar e commitar**

Run: `go test ./internal/transport/ -race`

```bash
git add internal/transport/
git commit -m "feat(transport): interface de transporte e implementacao local"
```

---

### Task R2: Cliente SSH portável

**Files:**
- Create: `internal/transport/ssh.go`, `internal/transport/agent_unix.go`, `internal/transport/agent_windows.go`, `internal/transport/sshconfig.go`
- Test: `internal/transport/ssh_test.go`, `internal/transport/sshconfig_test.go`

**Interfaces:**
- Consumes: `transport.Transport` (R1)
- Produces: `transport.SSHOptions` (`Host`, `Port`, `User`, `KeyPath`, `Password`, `KnownHostsPath`, `InsecureHostKey`, `Timeout`); `transport.SSH(opts SSHOptions) (Transport, error)`; `transport.ResolverSSHConfig(host string) (SSHOptions, error)`

- [ ] **Step 1: Investigar antes de escrever uma linha**

Este passo é leitura e experimento, não código. Determine e **registre no relatório** cada resposta, com a fonte:

1. **`ssh-agent` no Windows.** Qual é o caminho exato do named pipe usado pelo OpenSSH do Windows? Como conectar a ele em Go? Avalie `github.com/Microsoft/go-winio` — leia o `go.mod` dele e confirme que é Go puro sobre `x/sys/windows`, sem cgo. Se houver alternativa sem dependência nova, prefira. **Não presuma o caminho do pipe**: confirme na documentação do OpenSSH-Portable ou no fonte.
2. **`golang.org/x/crypto/ssh/agent`** — a função que cria o cliente a partir de uma conexão, e como transformar o cliente em `ssh.AuthMethod`.
3. **`golang.org/x/crypto/ssh/knownhosts`** — como construir o `HostKeyCallback`, e **qual erro exatamente** ele devolve quando o host é desconhecido versus quando a chave mudou. Os dois casos precisam de mensagens diferentes: chave desconhecida é atrito normal, chave alterada é possível ataque e a mensagem tem que dizer isso.
4. **`github.com/pkg/sftp`** — é Go puro? Como abrir um arquivo para leitura e como fazer glob (ele tem `Glob`?).
5. **Parser de `~/.ssh/config`** — existe biblioteca Go pura madura, tipo `github.com/kevinburke/ssh_config`? Ela resolve `Include`, `Match` e wildcards em `Host`? Se o suporte for parcial, decida e documente qual subconjunto o `ngx` honra — melhor honrar pouco e dizer, que honrar mal em silêncio.
6. **Caminho do `~/.ssh`** nas três plataformas: confirme que `os.UserHomeDir()` resolve corretamente no Windows.

Se qualquer item exigir cgo, pare e reporte antes de prosseguir.

- [ ] **Step 2: Escrever os testes de resolução de configuração e de host key**

`sshconfig_test.go` usa arquivos de `~/.ssh/config` em `t.TempDir()` e verifica: `HostName`, `User`, `Port` e `IdentityFile` sendo lidos; wildcard em `Host` casando; flag explícita sobrescrevendo o arquivo; host ausente do arquivo devolvendo os defaults sem erro.

`ssh_test.go` verifica a política de host key **sem rede**, chamando o callback diretamente: host presente no `known_hosts` com a chave certa passa; host ausente falha com erro que menciona o host e como adicioná-lo; host presente com chave **diferente** falha com erro que diz explicitamente que a chave do servidor mudou e que isso pode ser um ataque; e `InsecureHostKey` passando qualquer chave, mas registrando um diagnóstico de severidade `warning` no envelope — usar o escape não pode ser silencioso.

- [ ] **Step 3: Implementar**

Ordem de autenticação: `ssh-agent`, depois chave em arquivo (com prompt de passphrase se necessário), depois senha. Senha vem de `NGX_SSH_PASSWORD` ou de prompt em terminal — **nunca** de flag; se alguém adicionar uma flag de senha, o review deve reprovar.

Os arquivos `agent_unix.go` e `agent_windows.go` levam build tags e expõem a mesma função de conexão ao `ssh-agent`. Quando não houver `ssh-agent` disponível, isso não é erro: apenas aquele método de autenticação não entra na lista.

- [ ] **Step 4: Rodar e commitar**

Run: `go test ./internal/transport/ -race`, e `CGO_ENABLED=0 go build` para as seis plataformas.

```bash
git add internal/transport/
git commit -m "feat(transport): cliente ssh com known_hosts estrito e ssh-agent portavel"
```

---

### Task R3: Parse com `Glob` injetado

**Files:**
- Modify: `internal/config/parse.go` — passar `Glob` ao crossplane
- Test: `internal/config/parse_test.go`

**Interfaces:**
- Consumes: `config.ParseOptions` (Plano 1, Task 7)
- Produces: `ParseOptions.Glob func(pattern string) ([]string, error)`, injetado no `crossplane.ParseOptions`

- [ ] **Step 1: Confirmar a assinatura no crossplane**

Leia `crossplane.ParseOptions` no module cache e confirme o nome e a assinatura exata do campo `Glob`. Não escreva a partir de memória.

- [ ] **Step 2: Escrever o teste que falha**

Um teste com filesystem em memória contendo `nginx.conf` com `include conf.d/*.conf` e dois arquivos casando o padrão, **e** um arquivo com o mesmo nome no disco real que não deveria ser lido. Sem a correção, o crossplane lista o disco real; com ela, lista só o filesystem injetado.

Esse teste é a razão de esta tarefa existir: hoje, apontado para um host remoto, o `ngx` leria `conf.d/*.conf` da máquina do operador.

- [ ] **Step 3: Implementar e rodar**

Atenção: `parse.go` passou por três rodadas de correção e tem lógica delicada de concorrência e cache de fonte. A alteração aqui é acrescentar um campo e repassá-lo. **Não** toque em `leituraEspelhada`, `cacheFonte`, `coletarErros` nem no tratamento de erro.

Run: `go test ./internal/config/ -race`

- [ ] **Step 4: Commit**

```bash
git add internal/config/
git commit -m "fix(config): injeta Glob no crossplane para nao listar disco local"
```

---

### Task R4: Runtime remoto

**Files:**
- Modify: `internal/runtime/` — receber um `Transport` em vez de chamar `exec` direto
- Test: os testes existentes de runtime, mais casos com transporte falso

**Interfaces:**
- Consumes: `transport.Transport` (R1)
- Produces: as funções de runtime passando a aceitar um `Transport`

- [ ] **Step 1: Escrever os testes**

Use um `Transport` falso que devolve saídas gravadas, e verifique que a detecção do nginx, o `nginx -t` estruturado e o `nginx -T` funcionam idênticos com transporte local e remoto. O ponto é que o parser de saída não sabe de onde os bytes vieram.

Cubra também: `nginx` não encontrado no host remoto, e comando que sai diferente de zero.

- [ ] **Step 2: Refatorar o runtime**

Substitua as chamadas diretas a `exec.Command` por `Transport.Run`. Preserve a distinção da Task R1: código de saída diferente de zero é resultado, não erro.

Sobre o estado do processo: a leitura de pidfile funciona por SFTP, mas a contagem de workers e o horário de início do master dependem de inspeção de processo, que difere entre sistemas e é mais frágil por SSH. Mantenha a regra da spec — **campo indisponível é omitido, nunca estimado**.

- [ ] **Step 3: Rodar e commitar**

Run: `go test ./... -race`

```bash
git add internal/runtime/
git commit -m "refactor(runtime): executa via transporte, local ou remoto"
```

---

### Task R5: Flags globais e integração no CLI

**Files:**
- Modify: `internal/cli/root.go` — flags de conexão e construção do transporte
- Test: `internal/cli/root_test.go`

**Interfaces:**
- Consumes: `transport.SSH`, `transport.Local`, `transport.ResolverSSHConfig` (R1, R2)
- Produces: `cli.Context.Transport`

- [ ] **Step 1: Escrever os testes**

- Sem `--host`, o transporte é local e nada de SSH é construído.
- Com `--host`, os valores do `~/.ssh/config` são aplicados e as flags sobrescrevem.
- Uma flag de senha **não existe**; se alguém tentar `--password`, o cobra devolve erro de flag desconhecida.
- `--insecure-host-key` produz um diagnóstico de `warning` no envelope.
- O `meta` do envelope carrega contra qual host a operação rodou.

- [ ] **Step 2: Adicionar as flags**

`--host`, `--port`, `--user`, `--key`, `--insecure-host-key`, `--known-hosts`. O `--timeout` global já existe e passa a valer para a conexão.

Todo comando de leitura da v0.1 (`status`, `inspect`, `get`, `tree`, `test`, `diff`) funciona remoto sem alteração própria, porque recebe o transporte pelo contexto. `fmt --write` escreve por SFTP.

- [ ] **Step 3: Rodar e commitar**

Run: `go test ./... -race`

```bash
git add internal/cli/
git commit -m "feat(cli): flags de conexao remota e transporte no contexto"
```

---

### Task R6: Integração real e documentação

**Files:**
- Create: `internal/transport/integration_test.go` (`//go:build integration`), `docs/remoto.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: tudo acima
- Produces: suíte de integração contra SSH real, e documentação

- [ ] **Step 1: Escrever o teste de integração**

Sob build tag `integration`, suba um container com `sshd` e `nginx`, com uma chave de teste gerada no próprio teste, e verifique ponta a ponta: `inspect` remoto devolvendo a árvore do container; `include` com glob resolvendo os arquivos **do container**; `test` remoto reportando erro de sintaxe com arquivo e linha; e a recusa por host key desconhecida antes de o host ser adicionado ao `known_hosts`.

O caso do glob é o mais importante: é o defeito que a Task R3 corrigiu, e este é o teste que prova que ele não volta.

**A bancada tem que reproduzir a forma medida em produção, não um caso fácil.** Um container com um `nginx.conf` de dez linhas passa em tudo e não prova nada. Medido num nginx de produção real (Oracle Linux 9, nginx 1.20.1), a bancada precisa de:

- **Três padrões com curinga**, não um: `conf.d/*.conf`, `default.d/*.conf` e `modules/*.conf`. E, para o teste do glob valer, um arquivo homônimo no disco **local** que só apareceria se o `Glob` não estivesse injetado.
- **Ordem de 130 arquivos** na configuração efetiva, não três. É o número que torna a latência sequencial visível e que justifica o paralelismo por nível exigido na R4. Um teste que meça o tempo com 130 arquivos é o que impede a regressão de performance.
- **`nginx -T` legível só por root**, com `sudo` liberado sem senha para o usuário de teste — exatamente a armadilha da DR5. O teste tem que provar que sem `--sudo` o `ngx` reporta a exigência de privilégio, e que **não** escala sozinho.
- **Um segredo dentro da configuração** (chave privada, `auth_basic_user_file`) para exercitar a redação de ponta a ponta pelo caminho remoto.

A bancada é artefato do repositório, versionada, com alvo no `Makefile`. Quem clonar o projeto tem que conseguir rodar a integração com um comando. E os testes de integração ficam atrás da build tag: `go test ./...` sem a tag continua verde numa máquina sem Docker.

- [ ] **Step 2: Documentar**

Em `docs/remoto.md` e numa seção do `README.md`:

- O uso mínimo, `ngx --host web1.exemplo.com inspect`, explicando que funciona sem flags para quem já tem `ssh web1.exemplo.com` funcionando.
- Que **nada é instalado no servidor** — o `ngx` lê por SFTP e executa o nginx que já está lá.
- A ordem de autenticação, e que senha vem de `NGX_SSH_PASSWORD` ou de prompt, nunca de flag, explicando o porquê: flag vaza em `ps`, no histórico e em log de CI.
- Que host desconhecido é recusado, como adicionar ao `known_hosts`, e o que significa `--insecure-host-key` — inclusive que ele não deve virar hábito.
- No Windows, que o serviço `ssh-agent` vem desabilitado e precisa ser habilitado, com os comandos.
- A ressalva de latência: cada `include` é uma leitura de rede.

- [ ] **Step 3: Commit**

```bash
git add internal/transport/integration_test.go docs/remoto.md README.md
git commit -m "test(transport): integracao ssh real; docs de operacao remota"
```

---

## Verificação de cobertura

| Pedido | Task |
|---|---|
| Executar em servidor remoto via SSH | R1, R2, R4, R5 |
| Passar host, user, porta | R2 (`~/.ssh/config`), R5 (flags) |
| Senha, quando o servidor exigir | R2 (env ou prompt, nunca flag) |
| Chave SSH e caminho da chave | R2 (`--key`, `IdentityFile`, `ssh-agent` antes) |
| Não instalar o CLI na VM | DR3, R1 (SFTP mais exec remoto) |
| Funcionar em Linux, macOS e Windows | Global Constraints; R2 Step 1 item 1 |

## O que este plano não cobre

Multi-host numa chamada (`--hosts a,b,c` com execução paralela), bastion e salto (`ProxyJump`), túnel, e escrita transacional remota — esta última depende do plano de mutação da v0.2. São extensões naturais da mesma camada de transporte, e nenhuma muda as decisões tomadas aqui.
