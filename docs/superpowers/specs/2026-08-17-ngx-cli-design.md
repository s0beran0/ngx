# ngx v0.1 — Design

Data: 2026-08-17
Status: aprovado, pronto para plano de implementação
Baseado em: spec técnica `ngx` v1.0

---

## 1. Escopo

Este documento projeta a **v0.1** do `ngx`: a fundação e os comandos de leitura.
Nada nesta versão altera a configuração de um servidor em execução.

Entram na v0.1:

| Área | Entrega |
|---|---|
| Fundação | envelope JSON, exit codes, carregamento de config do ngx, redação |
| Parse | árvore canônica com IDs estáveis, spans de byte, resolução de `include` |
| Seletores | linguagem completa de leitura (`get`) |
| Runtime | detecção do nginx, `nginx -t` estruturado, sinal de drift |
| Comandos | `status`, `inspect`, `get`, `tree`, `fmt`, `test`, `diff` |

Ficam fora da v0.1, por versão: mutação e transação (v0.2), `lint` (v0.3),
`route` (v0.4), MCP (v0.5), `logs` e `upstreams` (v0.6).

A v0.1 é deliberadamente somente-leitura. As duas apostas mais arriscadas do
projeto — a linguagem de seletores e a estabilidade dos IDs — são validadas
antes que exista qualquer caminho de código capaz de escrever num `.conf` de
produção.

---

## 2. Decisões

Decisões tomadas durante o brainstorming, com a razão de cada uma. Elas são
premissas de tudo que vem depois.

### D1 — Preservação cirúrgica de formatação

Quando o `ngx` reescrever um `.conf`, comentários, espaçamento e estilo do autor
original permanecem byte a byte, exceto no trecho efetivamente alterado.

*Por quê:* a ferramenta edita arquivos que humanos mantêm. Um `apply` que
reformata o arquivo inteiro produz um diff ilegível, esconde a mudança real
dentro do ruído e destrói a confiança que a spec tenta construir com o resumo de
impacto. Um agente que reformata o arquivo do colega é um agente que ninguém
autoriza a rodar de novo.

*Consequência:* a árvore precisa carregar offsets de byte. Ver D2.

### D2 — Spans próprios sobre o crossplane

`nginx-go-crossplane` fornece a árvore semântica e a validação de diretivas.
Um tokenizador nosso, independente, fornece os offsets de byte. As duas
estruturas são casadas por sequência de tokens.

*Por quê:* o crossplane resolve casos de borda que levariam meses para
reimplementar — quoting, escapes, `map`, blocos Lua, diretivas de módulo — e
valida contexto e aridade de diretiva de graça. Mas seu `Directive` carrega
apenas `Line`, e mesmo o `NgxToken` do lexer (`{Value, Line, IsQuoted, Error}`)
não tem offset nem coluna. Nem D1 nem o campo `column` exigido em todo
diagnóstico saem do crossplane puro.

*Alternativas descartadas:* fork vendored do crossplane (menor esforço imediato,
mas manutenção de fork indefinida num projeto de uma pessoa); parser 100%
próprio (joga fora anos de casos de borda já resolvidos, contra o não-objetivo
de reimplementar o que funciona).

*Risco e mitigação:* o casamento token↔árvore é a parte frágil. É coberto por um
property test que sustenta a arquitetura inteira (§9). Se o property test se
mostrar impossível de satisfazer, o plano de contingência é propor os offsets
upstream no crossplane.

### D3 — IDs posicionais ancorados em hash

IDs são derivados da posição estrutural, contados **entre irmãos do mesmo tipo
de diretiva**. Todo envelope que devolve IDs carrega `config_hash` no `meta`. Um
ID apresentado junto a um hash diferente é rejeitado com exit 9.

*Por quê:* a spec v1.0 promete que o agente pode referenciar um nó entre
chamadas sem reler tudo, mas IDs puramente posicionais mudam de significado
quando um irmão anterior é inserido ou removido. A âncora de hash converte um
erro silencioso — o agente edita o nó errado — num erro explícito. É o princípio
"ambiguidade é erro, não palpite" aplicado ao tempo em vez do espaço, e reusa o
mecanismo de optimistic locking que a spec já define para patches.

### D4 — Drift por evidência, não por hash do master

`drift` é derivado da comparação entre o mtime dos arquivos de configuração e o
horário de início do processo master. `config_loaded_hash` só é reportado quando
o próprio `ngx` executou o reload e registrou o hash aplicado. Um campo
`drift_evidence` informa qual fonte respondeu.

*Por quê:* a spec v1.0 assume que `config_loaded_hash` é obtenível, e não é.
`nginx -T` lê do disco — despeja a configuração que o binário carregaria agora,
não a que o master tem em memória. Implementado como a spec descreve, o campo
sairia sempre idêntico ao hash do disco e `drift` seria constante `false`: o
campo que a spec chama de "ouro para um agente" mentiria em todos os casos. O
nginx OSS não expõe a configuração em memória por nenhuma flag ou sinal.

*Trade-off aceito:* o sinal de mtime sabe que algo mudou, não o quê. Em
compensação funciona no caso que mais importa — um humano editou o arquivo e não
recarregou — que a fonte exata não cobre.

### D5 — Redação no renderer

A redação de valores sensíveis acontece na serialização da saída, nunca na
árvore em memória.

*Por quê:* se a árvore fosse redigida no parse, `fmt` gravaria `***` dentro do
`.conf` do usuário. A redação existe para proteger o que sai para o contexto de
um LLM, não para mutilar o dado interno.

### D6 — Projeto pessoal, open source, MIT

Repositório pessoal do autor, licença MIT, sem vínculo institucional. CI e
empacotamento de release montados desde a v0.1.

---

## 3. Arquitetura

```
cmd/ngx/main.go          wiring e tradução de erro → exit code
internal/
  cli/       cobra: root, flags globais, os 7 comandos
  output/    envelope, renderers json|human, redação, exit codes
  config/    parse · spans · ids · combine · render · hash
  selector/  lexer · parser · eval
  runtime/   detect (-V) · test (-t) · dump (-T) · exec · process
  drift/     comparação disco ↔ carregado
  settings/  arquivo de configuração do ngx (koanf)
```

Pacotes de versões futuras — `plan`, `patch`, `snapshot`, `lint`, `route`,
`mcp`, `logs` — não são criados agora. Diretório vazio é dívida, não
arquitetura.

O que garante que eles caibam depois é a fronteira de `config`: ele devolve uma
árvore imutável, completa, com spans e IDs. Todo consumidor futuro é leitor
dessa árvore. Nenhum deles reabre arquivo, reparseia texto ou reimplementa
resolução de `include`.

**Regra de camada:** `cli/` não formata nada e `output/` não decide nada. Um
comando produz um valor tipado e, em caso de falha, um erro tipado que carrega
seu exit code. `output/` transforma isso em JSON, em texto humano e em código de
saída. É o que impede o envelope de virar `fmt.Println` espalhado por sete
arquivos e a tabela de exit codes de divergir entre comandos.

---

## 4. Modelo de dados

### 4.1 Nó

```go
type Span struct {
    Start int // offset de byte, inclusivo
    End   int // offset de byte, exclusivo
}

type Origin struct {
    File string
    Line int
}

type Node struct {
    Directive string
    Args      []string
    File      string
    Line      int
    Column    int
    Span      Span    // da primeira letra da diretiva ao ';' ou '}' final
    HeadSpan  Span    // apenas diretiva + args, sem o bloco
    ID        string
    Comment   *string
    Block     []*Node
    Origin    *Origin // preenchido em modo --combine
}
```

Dois spans e não um: `Span` é o intervalo que uma remoção apaga; `HeadSpan` é o
intervalo que uma substituição de argumentos reescreve. Ter os dois desde a v0.1
é o que torna a edição da v0.2 uma substituição de bytes em vez de uma
re-renderização do arquivo.

`Comment` é preenchido pelo crossplane com `ParseComments: true`; comentários
são nós de diretiva `#`.

### 4.2 Geração de IDs

Um ID é uma sequência de segmentos separados por `.`. Cada segmento é
`<abreviação><índice>`, onde o índice é a posição entre os irmãos **da mesma
diretiva**, base 0.

Tabela de abreviações:

| Diretiva | Abrev |
|---|---|
| `http` | `h` |
| `stream` | `st` |
| `events` | `e` |
| `mail` | `m` |
| `server` | `s` |
| `location` | `l` |
| `upstream` | `u` |
| `map` | `mp` |
| qualquer outra | nome completo da diretiva |

Diretivas simples (sem bloco) usam `d<N>` contado entre as diretivas simples
irmãs. Os blocos de contexto do nível raiz — `http`, `events`, `mail`, `stream`
— omitem o índice, por ocorrerem no máximo uma vez: o ID é `h`, não `h0`.

Exemplos: `h.s0`, `h.s0.d1`, `h.s1.l2`, `h.s1.l2.l0`, `h.u0`.

Contar entre irmãos do mesmo tipo, e não por posição absoluta, significa que
adicionar um `location` não renumera os `server` ao lado — degrada a
fragilidade sem eliminá-la. A eliminação vem da âncora de hash (D3).

### 4.3 Hash da configuração

`config_hash` é `sha256` da árvore normalizada em modo combine: diretivas e
argumentos serializados canonicamente, comentários e espaçamento excluídos. Duas
configurações que só diferem em formatação produzem o mesmo hash — o que é
correto, porque o que o hash protege é o significado, não o texto.

---

## 5. Linguagem de seletores

A gramática é a de §5 da spec v1.0. Este documento fixa as quatro regras de
desambiguação que a gramática deixa em aberto e que só aparecem ao implementar o
lexer.

### R1 — O `.` é separador apenas fora de colchetes

`http.server[server_name=api.exemplo.com]` — os pontos dentro do valor não
separam segmento. O lexer mantém profundidade de `[`. Valores podem ser aspeados
com `'` ou `"`, o que resolve locations regex e valores contendo `,` ou `]`:

```
location["~ \.php$"]
```

### R2 — `#` no início é ID; em qualquer outra posição é índice

`#h.s1.l2` é um literal de ID. `upstream#2` é o terceiro `upstream` (base 0,
conforme o exemplo da spec). A distinção é posicional: `#` como primeiro
caractere do seletor inteiro seleciona por ID.

### R3 — Predicado sobre o próprio nó vs. sobre um filho

Os exemplos da spec misturam os dois casos. A regra:

| Forma | Significado |
|---|---|
| `[/api]` | açúcar para `arg0=/api` — argumento do próprio nó |
| `[arg0=/api]`, `[arg1=ssl]` | argumento do próprio nó, explícito |
| `[server_name=api.com]` | diretiva **filha** `server_name` com algum arg casando |

Não há ambiguidade porque `argN` é reservado; qualquer outra chave só pode ser
nome de diretiva.

### R4 — Quantificação sobre múltiplos argumentos

`server_name a.com b.com` tem vários argumentos. `=`, `~` e `^=` casam se
**algum** argumento satisfizer o predicado. `!=` casa se **nenhum** argumento
satisfizer. A inversão do quantificador na negação é explícita porque deixá-la
implícita é uma fonte previsível de bug.

### Operadores

| Op | Semântica |
|---|---|
| `=` | igualdade exata |
| `~` | contém |
| `^=` | prefixo |
| `!=` | nenhum argumento é igual |

Predicados múltiplos dentro de um filtro são conjunção (E lógico).

---

## 6. Envelope, exit codes e redação

### 6.1 Envelope

Estrutura idêntica a §6 da spec v1.0:

```go
type Envelope struct {
    OK          bool         `json:"ok"`
    Command     string       `json:"command"`
    NgxVersion  string       `json:"ngx_version"`
    Data        any          `json:"data"`
    Diagnostics []Diagnostic `json:"diagnostics"`
    Meta        Meta         `json:"meta"`
}
```

`Meta` da v0.1 carrega `duration_ms`, `nginx_version` e `config_hash` (D3).

`Diagnostic` carrega `severity`, `code`, `message`, `file`, `line`, `column`,
`selector` e `docs`. O campo `fix` existe na struct mas permanece vazio na v0.1,
já que nenhum comando desta versão produz patches.

JSON é o padrão quando stdout não é TTY; `--human` e `--json` forçam.

### 6.2 Exit codes

A v0.1 emite apenas os códigos que seus comandos podem produzir:

| Código | Significado |
|---|---|
| 0 | sucesso |
| 1 | erro interno / IO |
| 2 | erro de uso (flag inválida, seletor malformado) |
| 3 | configuração inválida (`nginx -t` falhou) |
| 7 | drift detectado |
| 9 | `config_hash` divergente do ID apresentado |

Os códigos 4, 5, 6 e 8 pertencem a comandos que ainda não existem e não são
documentados como suportados até que sejam emitíveis. Código de saída
documentado mas nunca emitido é pior que ausente: um agente escreve tratamento
para um caso que jamais ocorre e deixa de tratar o que ocorre.

Comandos não escolhem exit code. Cada erro é um tipo que carrega o seu, e um
único ponto em `main.go` traduz.

### 6.3 Redação

Configurada em `output.redact`. Os três formatos que a spec usa como exemplo são
unificados num único matcher — nome de diretiva com prefixo de argumentos
opcional:

```yaml
redact:
  - ssl_certificate_key                 # por nome de diretiva
  - proxy_set_header Authorization      # nome + prefixo de argumentos
  - "**.auth_basic_user_file"           # prefixo de contexto, aceito e redundante
```

O prefixo `**.` é aceito para compatibilidade com a spec, mas é redundante:
matchers já valem em qualquer contexto. Aceitá-lo evita que uma configuração
escrita a partir da spec falhe silenciosamente.

Comportamento:

- O **valor** é substituído por `***`; a diretiva, o `id` e a linha permanecem.
  Remover o nó inteiro faria o agente concluir que a diretiva não existe, o que
  é pior que esconder o valor.
- `diff` passa pela redação como qualquer outra saída. É o ponto mais fácil de
  vazar sem perceber.
- `fmt` escrevendo em disco **não** redige (D5).
- `--no-redact` é aceito somente quando stdout é TTY. Um humano depurando vê o
  segredo; um agente lendo o pipe, estruturalmente, não consegue. Custa poucas
  linhas e fecha o furo que a redação existe para fechar.

---

## 7. Runtime

Todas as invocações do nginx usam `exec.Command` com argv explícito. Nenhuma
string é interpolada em shell.

### 7.1 Detecção

`nginx -V` escreve em **stderr**. Dele são extraídos `prefix` (`--prefix=`),
`main_config` (`--conf-path=`), o caminho do pidfile (`--pid-path=`), a versão e
os módulos estáticos (`--with-*_module`).

Módulos **dinâmicos** carregados via `load_module` não aparecem em `-V`. A lista
de módulos é complementada a partir da árvore, senão `modules` fica incompleta
exatamente nos casos não triviais.

### 7.2 Estado do processo

- `running` e `master_pid`: leitura do pidfile e sinal 0. Portável, sem
  dependência nova.
- `workers` e `config_loaded_at`: exigem inspeção de processo, que diverge entre
  Linux e darwin. **Campo indisponível é omitido do JSON, nunca estimado.** Um
  agente trata a ausência de um campo muito melhor que um número errado.

### 7.3 `nginx -t` estruturado

A saída de erro tem a forma:

```
nginx: [emerg] unknown directive "foo" in /etc/nginx/conf.d/a.conf:3
nginx: configuration file /etc/nginx/nginx.conf test failed
```

O parser converte cada linha em um `Diagnostic` e, usando a árvore já
carregada, **traduz `file:line` de volta para um `selector` e um `id`**.

Isso realiza o item 1 do Apêndice B da spec: o agente recebe o erro do próprio
nginx já endereçado na linguagem com que ele opera a ferramenta, sem reparsear
nada. Sai quase de graça porque os spans já existem.

### 7.4 Drift

Conforme D4:

```json
{
  "drift": true,
  "drift_evidence": "mtime",
  "config_on_disk_hash": "sha256:ab12...",
  "config_loaded_hash": null
}
```

`drift_evidence` assume `mtime` (arquivos modificados após o início do master)
ou `hash` (o `ngx` registrou o reload e comparou conteúdo). Quando nenhuma fonte
consegue responder — master não está rodando, por exemplo — `drift` é `null`, e
não `false`.

Comparação de drift nunca é textual: `nginx -T` não bate byte a byte com o
disco. Quando houver comparação de conteúdo, ela é entre árvores normalizadas.

---

## 8. Comandos da v0.1

Flags globais conforme §4.1 da spec: `--config/-c`, `--json`, `--human`,
`--quiet/-q`, `--no-color`, `--nginx-bin`, `--nginx-version`, `--timeout`,
`--profile`.

| Comando | Contrato | Exit |
|---|---|---|
| `status` | estado de runtime + drift | 0, ou 7 se drift |
| `inspect` | runtime + árvore completa + resumo | 0, 3 |
| `get <seletor>` | subconjunto da árvore; seletor obrigatório | 0, 2 se malformado, 9 se hash divergente |
| `tree` | hierarquia resumida de server/location/upstream com IDs | 0 |
| `fmt` | formata; `--check` não escreve, `--write` escreve | 0, ou 7 com `--check` se houver diferença |
| `test` | wrapper estruturado de `nginx -t` | 0, 3 |
| `diff` | drift textual e/ou o que `fmt` mudaria | 0, ou 7 se houver diferença |

`get` sem seletor é erro de uso, não dump total — coerente com o princípio de
economia de contexto, e antecipa a restrição que o MCP tornará obrigatória.

`fmt` é o único comando da v0.1 que escreve em disco, e apenas com `--write`
explícito. A escrita é atômica: arquivo temporário no mesmo filesystem, `fsync`,
`rename`, com preservação de permissões.

Sobre `diff` na v0.1: a spec lista `diff` no roadmap da v0.1 mas o descreve em
§4 como comando de transação ("diff textual do que mudaria"). Sem mutação nesta
versão não existe "o que mudaria". A leitura adotada é a que faz sentido sem
escrita: diff de drift e diff de formatação.

### 8.1 Arquivo de configuração

A v0.1 carrega, via koanf, o subconjunto de §13 que seus comandos usam:
`nginx.binary`, `nginx.config`, `output.format` e `output.redact`. O local
(`./.ngx/config.yaml`) sobrescreve o global (`/etc/ngx/ngx.yaml`). Chaves de
versões futuras são ignoradas sem erro, para que um arquivo escrito a partir da
spec completa funcione hoje.

---

## 9. Testes

Desenvolvimento guiado por testes, do primeiro commit.

**Property test que sustenta a arquitetura.** Para qualquer `.conf` do corpus:
os spans de todos os nós, mais os intervalos entre eles, reconstituem o arquivo
**byte a byte**. Se essa propriedade vale, o casamento token↔árvore de D2 está
correto e a edição cirúrgica da v0.2 é segura. Se quebra, quebra alto e cedo,
antes de existir qualquer código capaz de escrever em produção. É o teste que
justifica ter escolhido a abordagem D2 em vez do fork.

**Fuzzing.** No lexer e no parser de seletores, e no alinhamento token↔árvore.

**Golden files.** Corpus de `.conf` reais, incluindo os do repositório de testes
do crossplane, serializados em JSON com árvore, spans e IDs. Flag `-update` para
regeneração.

**Fake nginx.** Um binário Go compilado pelo próprio teste, com cenários
dirigidos por variável de ambiente. Exercita o parse de `-V`, `-t` e `-T`,
incluindo os caminhos de erro, sem Docker e sem shell.

**Integração.** Container com nginx real, sob `//go:build integration`, fora do
`go test` padrão. Valida que a detecção e o parse de erro funcionam contra o
binário de verdade, e não apenas contra o fake.

---

## 10. Repositório e distribuição

- Módulo Go: `github.com/s0beran0/ngx` — **a confirmar** antes do primeiro
  push; derivado do caminho local, não do handle real do GitHub.
- Toolchain fixado em `.tool-versions`: `golang 1.25.9`.
- Licença MIT, em nome pessoal do autor.
- CI no GitHub Actions: build, `go vet`, testes com race detector, lint.
- Release com goreleaser: cross-compile para linux/amd64, linux/arm64 e darwin.
  Zero CGO, binário único estático.

Distribuição, canais de release e auto-atualização têm plano próprio em
`docs/superpowers/plans/2026-08-17-ngx-distribuicao.md`, com três decisões que
estendem esta seção: canais derivados de semver (tag limpa é stable, sufixo
`-beta`/`-rc` é pré-lançamento), verificação de release por checksum SHA256 mais
assinatura minisign, e chave pública embutida no binário em tempo de compilação.
O comando `ngx update` não constava de §4 e passa a existir.

O motivo da assinatura, e não apenas do checksum: o `ngx` roda como root em
servidores que servem tráfego. Um auto-update verificado só por checksum aceita
qualquer binário de quem consiga publicar um release, porque o atacante publica
o checksum dele junto. A assinatura mantém a garantia mesmo com a conta do
GitHub comprometida.

Acesso remoto via SSH tem plano próprio em
`docs/superpowers/plans/2026-08-17-ngx-remoto-ssh.md`, antecipando parte do
"multi-host via SSH" que §16 coloca na v1.0. Ele opera sem instalar nada no
servidor: lê a configuração por SFTP e executa o nginx que já existe lá. Três
decisões: verificação estrita de host key com escape explícito, autenticação
tentando o `ssh-agent` antes de qualquer arquivo de chave, e `~/.ssh/config`
respeitado para que `ngx --host web1` funcione para quem já tem `ssh web1`.

Esse plano também corrige um defeito da v0.1 que só se torna visível no uso
remoto: o `ngx` injeta `Open` no crossplane mas não `Glob`, então
`include conf.d/*.conf` é resolvido com `filepath.Glob` sobre o disco local.
Apontado para um host remoto, o `ngx` listaria arquivos da máquina do operador
e os trataria como configuração do servidor.

Dependências da v0.1:

| Uso | Pacote |
|---|---|
| parse de configuração | `github.com/nginxinc/nginx-go-crossplane` |
| CLI | `github.com/spf13/cobra` |
| configuração do ngx | `github.com/knadh/koanf` |
| diff | `github.com/hexops/gotextdiff` |
| testes | `github.com/stretchr/testify` |

---

## 11. Divergências em relação à spec v1.0

Registradas para que a spec possa ser atualizada.

| # | Ponto | Divergência |
|---|---|---|
| 1 | §3.2 `config_loaded_hash` | Não é obtenível: `nginx -T` lê do disco, não da memória do master. Substituído pelo modelo de evidência (D4). |
| 2 | §3.1 IDs | Ancorados em `config_hash`, com rejeição explícita quando o hash diverge (D3). |
| 3 | §5 gramática | Quatro ambiguidades resolvidas por regra explícita (R1–R4). |
| 4 | §6 exit codes | A v0.1 documenta apenas os códigos que emite. |
| 5 | §13 `redact` | Os três formatos unificados num único matcher; `**.` aceito como redundante. |
| 6 | §14 redação | Adicionado: `--no-redact` recusado quando stdout não é TTY. |
| 7 | roadmap v0.1 | `diff` reinterpretado como drift + formatação, já que não há mutação nesta versão. |

---

## 12. Fora de escopo

Não fazem parte deste design: qualquer caminho de código que altere um `.conf`
de produção, reload, snapshot, rollback, lint, simulação de roteamento, servidor
MCP, leitura de logs e orquestração multi-nó. Cada um entra pela versão do
roadmap correspondente, com seu próprio ciclo de design e plano.
