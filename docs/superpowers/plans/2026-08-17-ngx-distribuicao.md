# ngx — Plano de Distribuição: CI, releases, instalação e auto-update

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publicar o `ngx` de forma que um operador instale com um comando, atualize com `ngx update`, e possa verificar que o binário que recebeu é o que foi publicado.

**Architecture:** GitHub Actions roda a suíte em todo PR e push; uma tag dispara o goreleaser, que compila para quatro plataformas, gera `checksums.txt` e o assina com minisign. A chave pública fica embutida no binário, então `ngx update` verifica assinatura e checksum antes de substituir a si mesmo. Canais saem de semver: tag limpa é stable, tag com sufixo de pré-lançamento é beta.

**Tech Stack:** GitHub Actions, goreleaser v2, minisign, `aead.dev/minisign` v0.3.0.

**Spec:** `docs/superpowers/specs/2026-08-17-ngx-cli-design.md` (§10 Repositório e distribuição)

**Pré-requisito:** o Plano 1 precisa estar concluído — em particular a Task 14, que roda o `go mod tidy` definitivo. Este plano assume um `go.mod` estável.

## Global Constraints

- Módulo Go: `github.com/eduardoborges/ngx`. Go 1.25.
- **Zero CGO.** Todo build usa `CGO_ENABLED=0`. Qualquer dependência nova precisa ser Go puro — verifique antes de adicionar.
- Licença MIT em nome de Eduardo Benck. Nenhuma menção a SEA Tecnologia.
- **Mensagens de commit nunca mencionam Claude ou IA.** Sem trailer `Co-Authored-By`, sem "Generated with".
- Comentários de código em português, sem acentuação.
- Plataformas: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`,
  `windows/amd64`, `windows/arm64`.
- Arquivo de distribuição: `.tar.gz` em Linux e macOS, `.zip` no Windows.

## Decisões

Tomadas antes de escrever este plano; são premissas do resto.

### DD1 — Canais por semver, não por branch

Tag `v0.2.0` é stable. Tag `v0.2.0-beta.1` ou `v0.2.0-rc.1` é pré-lançamento, marcada como tal no GitHub. O goreleaser faz isso sozinho com `prerelease: auto`, que inspeciona o sufixo da tag.

*Por quê:* não exige manter duas branches em sincronia nem backportar correção entre elas. É o que a maior parte do ecossistema Go faz, então o comportamento não surpreende ninguém.

### DD2 — Verificação por checksum mais assinatura minisign

O goreleaser gera `checksums.txt` com o SHA256 de cada artefato e o assina com minisign, produzindo `checksums.txt.minisig`. A chave pública é embutida no binário em tempo de compilação. O `ngx update` verifica a assinatura do `checksums.txt`, depois confere o SHA256 do arquivo baixado contra ele, e só então substitui o binário.

*Por quê:* o `ngx` roda como root em servidores que servem tráfego. Um auto-update sem verificação transforma qualquer comprometimento da cadeia de distribuição em execução de código como root em todo servidor que atualizar. Só checksum não basta: quem consiga publicar um release publicaria o checksum do próprio binário junto. A assinatura protege mesmo com a conta do GitHub comprometida, porque a chave privada vive fora dela.

*Custo aceito:* há uma chave privada para guardar. Perdê-la significa que updates existentes param de aceitar releases novas até um binário com a chave nova ser distribuído por outro caminho.

### DD3 — A chave pública é embutida, não baixada

Uma chave pública que o próprio `update` baixa não protege contra nada: quem controla o servidor entrega a chave dele junto com o binário dele. Ela entra via `-ldflags -X` no build.

### DD4 — Windows é suportado, com ressalva documentada

O `ngx` compila e roda em Windows, porque o nginx para Windows existe e é distribuído oficialmente. Mas o próprio nginx.org classifica aquela build como **beta**: usa apenas `select()`/`poll()`, "alta performance e escalabilidade não devem ser esperadas", apenas um worker efetivamente trabalha, e não há suporte a UDP nem QUIC.

*Consequência prática:* o binário do nginx no Windows não é instalado por gerenciador de pacotes — fica solto num diretório desempacotado, tipo `C:\nginx-1.31.3\nginx.exe`, e usa o diretório de execução como prefixo. Então a detecção automática de caminhos que funciona em Linux não se aplica, e a documentação precisa mostrar como apontar o `ngx` para o diretório certo com `-c`.

*O README diz isso ao usuário.* Suportar a plataforma e ser honesto sobre suas limitações não são coisas contraditórias — omitir levaria alguém a apostar produção numa build que o próprio fornecedor não recomenda.

### DD5 — No Windows, o binário em execução é renomeado, não sobrescrito

O Windows trava o executável em execução: renomear funciona, deletar não. O `Apply` do Windows renomeia o binário atual para `.old`, coloca o novo no lugar, e a remoção do `.old` fica para a execução seguinte do `ngx`, que a faz na inicialização.

*Por quê:* a lógica de `rename` atômico que funciona em Linux e macOS falharia no Windows, e o modo de falha seria um update que aborta no meio, deixando o usuário sem binário funcional. Não é detalhe de portabilidade — é a diferença entre atualizar e quebrar a instalação.

---

### Task D1: Integração contínua

**Files:**
- Create: `.github/workflows/ci.yml`
- Test: o próprio workflow, verificado num push

**Interfaces:**
- Consumes: `go.mod`, a suíte de testes do Plano 1
- Produces: um workflow `ci` que roda em todo push para `main` e em todo pull request

- [ ] **Step 1: Escrever o workflow**

Criar `.github/workflows/ci.yml`:

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      - name: vet
        run: go vet ./...

      - name: testes com race detector
        run: go test ./... -race

      # O binario e distribuido estatico: uma dependencia que exija cgo
      # quebraria o cross-compile, e o erro so apareceria no release.
      - name: build sem cgo
        env:
          CGO_ENABLED: 0
        run: go build ./...

  cross:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        goos: [linux, darwin, windows]
        goarch: [amd64, arm64]
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      - name: compila para ${{ matrix.goos }}/${{ matrix.goarch }}
        env:
          CGO_ENABLED: 0
          GOOS: ${{ matrix.goos }}
          GOARCH: ${{ matrix.goarch }}
        run: go build -o /dev/null ./cmd/ngx
```

- [ ] **Step 2: Verificar que o workflow é válido**

Run: `gh workflow view ci` depois do push, ou valide localmente com `act -n` se disponível.
Expected: o workflow aparece e os dois jobs são reconhecidos.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: roda vet, testes com race e cross-compile"
```

---

### Task D2: Release por tag, com canais e assinatura

**Files:**
- Create: `.goreleaser.yaml`, `.github/workflows/release.yml`
- Modify: `internal/output/envelope.go` — expor a variável de chave pública para o `-ldflags`

**Interfaces:**
- Consumes: `output.Version` (Plano 1, Task 1)
- Produces: `output.PublicKey` (string, preenchida no build); artefatos de release `ngx_<versão>_<os>_<arch>.tar.gz`, `checksums.txt`, `checksums.txt.minisig`

- [ ] **Step 1: Gerar o par de chaves minisign**

Este passo é feito **uma vez, pelo dono do repositório**, fora do CI:

```bash
minisign -G -p ngx-minisign.pub -s ngx-minisign.key
```

Guarde a chave privada e a senha em local seguro, e adicione dois secrets no repositório do GitHub: `MINISIGN_KEY` com o conteúdo do arquivo `.key`, e `MINISIGN_PASSWORD` com a senha. A chave pública (`ngx-minisign.pub`) vai versionada no repositório e embutida no binário.

Se o implementador não tiver acesso aos secrets, ele para aqui e reporta — não invente chave.

- [ ] **Step 2: Expor a variável da chave pública**

Em `internal/output/envelope.go`, junto de `Version`:

```go
// PublicKey e a chave publica minisign usada para verificar releases.
// Preenchida no build via -ldflags; vazia em build local, o que faz o
// comando update recusar atualizar em vez de aceitar sem verificacao.
var PublicKey = ""
```

- [ ] **Step 3: Escrever a configuração do goreleaser**

Criar `.goreleaser.yaml`:

```yaml
version: 2
project_name: ngx

before:
  hooks:
    - go mod tidy

builds:
  - main: ./cmd/ngx
    binary: ngx
    env:
      - CGO_ENABLED=0
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X github.com/eduardoborges/ngx/internal/output.Version={{ .Version }}
      - -X github.com/eduardoborges/ngx/internal/output.PublicKey={{ .Env.NGX_PUBLIC_KEY }}

archives:
  - formats: [tar.gz]
    # Windows recebe .zip: e o que a plataforma abre sem ferramenta extra, e
    # o que o install.ps1 espera.
    format_overrides:
      - goos: windows
        formats: [zip]
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    files:
      - LICENSE
      - README.md

checksum:
  name_template: checksums.txt
  algorithm: sha256

# Assinar apenas o checksums.txt e suficiente: ele cobre todos os artefatos
# por hash, entao uma assinatura protege o conjunto inteiro.
signs:
  - id: minisign
    cmd: minisign
    args: ["-S", "-s", "{{ .Env.MINISIGN_KEY_FILE }}", "-m", "${artifact}", "-x", "${signature}"]
    signature: "${artifact}.minisig"
    artifacts: checksum
    stdin: "{{ .Env.MINISIGN_PASSWORD }}"

release:
  # Marca a release como pre-release quando a tag tem sufixo de pre-lancamento
  # (-beta, -rc, -alpha). E o que separa o canal beta do stable.
  prerelease: auto

changelog:
  sort: asc
  filters:
    exclude:
      - "^docs:"
      - "^test:"
      - "^chore:"
```

> A sintaxe exata de `signs.args` para minisign precisa ser confirmada contra a documentação do goreleaser v2 e o `minisign -h` da versão instalada no runner. Rode um release de teste com `--snapshot --clean` localmente antes de criar a primeira tag, e ajuste se o minisign reclamar dos argumentos. Não adivinhe: o erro só apareceria na primeira release de verdade.

- [ ] **Step 4: Escrever o workflow de release**

Criar `.github/workflows/release.yml`:

```yaml
name: release

on:
  push:
    tags: ["v*"]

permissions:
  contents: write

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      - name: instala o minisign
        run: |
          sudo apt-get update
          sudo apt-get install -y minisign

      - name: prepara a chave de assinatura
        env:
          MINISIGN_KEY: ${{ secrets.MINISIGN_KEY }}
        run: |
          umask 077
          printf '%s' "$MINISIGN_KEY" > "$RUNNER_TEMP/minisign.key"

      - uses: goreleaser/goreleaser-action@v6
        with:
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          MINISIGN_PASSWORD: ${{ secrets.MINISIGN_PASSWORD }}
          MINISIGN_KEY_FILE: ${{ runner.temp }}/minisign.key
          NGX_PUBLIC_KEY: ${{ vars.NGX_PUBLIC_KEY }}
```

`NGX_PUBLIC_KEY` é uma **variable** do repositório (não secret — é pública por definição), contendo a linha de chave do arquivo `.pub`, sem o comentário do cabeçalho.

- [ ] **Step 5: Testar em snapshot antes de qualquer tag**

Run: `goreleaser release --snapshot --clean --skip=sign`
Expected: gera `dist/` com os quatro binários e o `checksums.txt`. Confirme com `file dist/ngx_linux_amd64_v1/ngx` que é estático e sem interpretador dinâmico.

- [ ] **Step 6: Commit**

```bash
git add .goreleaser.yaml .github/workflows/release.yml internal/output/envelope.go ngx-minisign.pub
git commit -m "release: goreleaser com canais por semver e assinatura minisign"
```

---

### Task D3: Script de instalação

**Files:**
- Create: `install.sh`
- Test: `install_test.sh` (roda o script contra um diretório temporário)

**Interfaces:**
- Consumes: os artefatos publicados pela Task D2
- Produces: `install.sh`, executável via `curl -fsSL https://raw.githubusercontent.com/eduardoborges/ngx/main/install.sh | sh`

- [ ] **Step 1: Escrever o script**

Criar `install.sh`. Requisitos que o script precisa satisfazer, e que os testes do Step 2 verificam:

- Detecta sistema e arquitetura via `uname -s` e `uname -m`, mapeando para os nomes que o goreleaser usa (`x86_64` → `amd64`, `aarch64`/`arm64` → `arm64`). Recusa combinação não suportada com mensagem clara.
- Resolve a última release **stable** por padrão, consultando `https://api.github.com/repos/eduardoborges/ngx/releases/latest` — esse endpoint já exclui pré-lançamentos. Aceita `NGX_CHANNEL=beta`, que passa a listar `/releases` e pega a primeira entrada, e `NGX_VERSION=v0.2.0` para versão fixa.
- Baixa o tarball, o `checksums.txt` e confere o SHA256 antes de extrair. Usa `sha256sum` ou `shasum -a 256`, o que existir.
- Instala em `/usr/local/bin` por padrão, respeitando `NGX_INSTALL_DIR`.
- **Checa a permissão de escrita ANTES de baixar qualquer coisa**, e se faltar, aborta com a instrução exata — sem chamar `sudo` sozinho. Um script que escala privilégio por conta própria é exatamente o que ninguém deveria executar via `curl | sh`, e checar antes evita gastar o download para falhar no fim:

```sh
if [ ! -w "$NGX_INSTALL_DIR" ]; then
    echo "erro: sem permissao de escrita em $NGX_INSTALL_DIR" >&2
    echo "" >&2
    echo "rode a instalacao com privilegio:" >&2
    echo "  curl -fsSL https://raw.githubusercontent.com/eduardoborges/ngx/main/install.sh | sudo sh" >&2
    echo "" >&2
    echo "ou instale num diretorio seu, sem privilegio:" >&2
    echo "  curl -fsSL https://raw.githubusercontent.com/eduardoborges/ngx/main/install.sh | NGX_INSTALL_DIR=\$HOME/.local/bin sh" >&2
    exit 1
fi
```

  As duas saídas importam: quem está numa máquina onde não tem root precisa da segunda tanto quanto quem tem precisa da primeira.
- Usa `set -eu`, limpa o diretório temporário com `trap`, e funciona em `sh` puro — não assume bash.

> A verificação de assinatura minisign **não** entra no script: exigiria o minisign instalado antes da instalação. O checksum protege contra download corrompido e a origem é HTTPS do GitHub. Quem quiser a garantia forte baixa manualmente e verifica, ou instala uma vez e usa `ngx update` daí em diante, que verifica assinatura. Documente essa diferença no README.

- [ ] **Step 2: Escrever o teste do script**

Criar `install_test.sh`, que exercita o script sem tocar o sistema:

- Instala em `NGX_INSTALL_DIR` apontando para um diretório temporário e confirma que o binário aparece lá e responde a `ngx version`.
- Confirma que arquitetura não suportada falha com código diferente de zero e mensagem mencionando a plataforma.
- Confirma que checksum divergente aborta a instalação: baixe, corrompa o tarball e verifique que o script recusa.
- Confirma que `NGX_VERSION` fixa de fato instala aquela versão.

- [ ] **Step 3: Escrever o script de instalação do Windows**

Criar `install.ps1`, equivalente em PowerShell. Requisitos:

- Detecta a arquitetura por `$env:PROCESSOR_ARCHITECTURE` (`AMD64` → `amd64`, `ARM64` → `arm64`).
- Baixa o `.zip` — não o `.tar.gz` — e o `checksums.txt`, confere o SHA256 com `Get-FileHash -Algorithm SHA256`, e só então extrai com `Expand-Archive`.
- Instala em `$env:LOCALAPPDATA\ngx\bin` por padrão, respeitando `$env:NGX_INSTALL_DIR`. Esse diretório é gravável sem elevação, que é o comportamento certo para a plataforma — diferente do Unix, no Windows não há um `/usr/local/bin` convencional.
- **Acrescenta o diretório ao `PATH` do usuário** se ainda não estiver lá, via `[Environment]::SetEnvironmentVariable('Path', ..., 'User')`, e avisa que é preciso abrir um terminal novo para a mudança valer. Sem isso, a pessoa instala e o comando não é encontrado.
- Se o usuário apontar `NGX_INSTALL_DIR` para um caminho que exija elevação, como `C:\Program Files`, detecta a falta de permissão **antes de baixar** e instrui a rodar o PowerShell como administrador — mesma regra do Unix, sem tentar elevar sozinho.
- Aceita `$env:NGX_CHANNEL` e `$env:NGX_VERSION`, com o mesmo significado da versão Unix.

O one-liner documentado no README:

```powershell
irm https://raw.githubusercontent.com/eduardoborges/ngx/main/install.ps1 | iex
```

- [ ] **Step 4: Rodar**

Run: `sh install_test.sh`
Expected: todos os casos passam, e nada foi escrito fora do diretório temporário.

O `install.ps1` não tem teste automatizado neste plano — exigiria um runner Windows, e o CI da Task D1 não tem um. Verifique-o à mão numa máquina Windows, ou num runner `windows-latest` avulso, e registre no relatório o que foi testado e o que não foi. **Não** reporte como verificado o que você não conseguiu rodar.

- [ ] **Step 5: Commit**

```bash
git add install.sh install.ps1 install_test.sh
git commit -m "feat: scripts de instalacao para unix e windows"
```

---

### Task D4: Comando `ngx update`

**Files:**
- Create: `internal/update/update.go`, `internal/update/github.go`, `internal/update/verify.go`, `internal/cli/update.go`
- Test: `internal/update/update_test.go`, `internal/update/verify_test.go`, `internal/cli/update_test.go`
- Modify: `internal/cli/root.go` — registrar o comando

**Interfaces:**
- Consumes: `output.Version`, `output.PublicKey` (Task D2); `cli.Context`, `output.New`, os construtores de erro (Plano 1)
- Produces: `update.Release` (`Version`, `Prerelease bool`, `Assets []Asset`); `update.Channel` com `ChannelStable`/`ChannelBeta`; `update.Latest(ctx, channel) (*Release, error)`; `update.Verify(dados, checksums, assinatura []byte, chavePublica, nomeArquivo string) error`; `update.Apply(caminhoBinario string, novo []byte) error`

- [ ] **Step 1: Investigar antes de escrever**

Este passo é leitura, não código. Antes de implementar, determine e anote no relatório:

1. **`aead.dev/minisign` v0.3.0** — a assinatura exata de `Verify`, como obter uma `PublicKey` a partir da string embutida, e **se o módulo tem dependência não-stdlib que exija cgo**. Leia o `go.mod` dele no module cache depois de `go get`. Se exigir cgo, pare e reporte: a restrição de build estático é inegociável e a alternativa seria verificar Ed25519 direto com `crypto/ed25519`, decodificando o formato do minisign à mão.
2. **Formato do `checksums.txt` do goreleaser** — a ordem das colunas e o separador, para o parser não depender de suposição. Gere um com `goreleaser release --snapshot --clean` e leia.
3. **Substituição do binário em execução** — em Linux e macOS, `rename(2)` sobre um binário em execução funciona porque o inode antigo sobrevive enquanto houver descritor aberto, mas escrever *por cima* falha com `ETXTBSY`. Confirme o comportamento e escreva o teste que o cobre.
4. **O mesmo, no Windows** — lá o executável em execução é travado: renomear funciona, **deletar não**. Confirme e determine se `os.Rename` do Go basta ou se é preciso `MoveFileEx` via `golang.org/x/sys/windows`. Referência útil: a estratégia de `github.com/minio/selfupdate`, que renomeia o binário atual para `.old`, põe o novo no lugar, e não consegue apagar o `.old` — deixando a limpeza para depois. Não precisamos adotar a biblioteca; precisamos entender a técnica.

Registre as quatro respostas no relatório antes de seguir. Não escreva código a partir de suposição sobre nenhuma delas — em particular sobre a 4, porque o modo de falha é um update que aborta no meio e deixa o usuário sem binário funcional.

- [ ] **Step 2: Escrever os testes de verificação**

`internal/update/verify_test.go` precisa cobrir, no mínimo:

- Assinatura válida e checksum correto: aceita.
- Assinatura válida mas checksum do arquivo diverge: recusa, com erro citando o nome do arquivo.
- Assinatura inválida para o `checksums.txt`: recusa **sem sequer olhar o checksum** — a ordem importa, porque conferir hash contra um `checksums.txt` não autenticado não prova nada.
- Chave pública vazia (build local, sem `-ldflags`): recusa com erro explicando que este binário não foi construído para se auto-atualizar. **Nunca** cair para "aceitar sem verificar".
- Nome de arquivo ausente do `checksums.txt`: recusa.

Gere um par de chaves de teste no próprio teste e assine o conteúdo de teste, em vez de embutir chave fixa.

- [ ] **Step 3: Implementar a verificação**

`internal/update/verify.go`, seguindo a ordem: parse da chave pública embutida → verificação da assinatura sobre os bytes do `checksums.txt` → parse do `checksums.txt` → comparação do SHA256 do artefato. Qualquer falha aborta, e nenhuma delas tem caminho de bypass.

- [ ] **Step 4: Implementar a consulta ao GitHub**

`internal/update/github.go`. Sem dependência nova: `net/http` e `encoding/json` bastam.

- Canal stable usa `/releases/latest`, que o GitHub já filtra para excluir pré-lançamentos.
- Canal beta usa `/releases` e pega a primeira entrada, que a API devolve ordenada por data de criação decrescente.
- Respeite o `--timeout` global do CLI no `http.Client`.
- Trate 403 com `X-RateLimit-Remaining: 0` como erro específico, dizendo ao usuário que o limite da API foi atingido — é o erro mais provável em uso real e um "falhou" genérico manda a pessoa procurar no lugar errado.

- [ ] **Step 5: Implementar a substituição, com caminhos separados por sistema**

O comportamento diverge entre Unix e Windows o bastante para justificar dois arquivos com build tags: `internal/update/apply_unix.go` (`//go:build !windows`) e `internal/update/apply_windows.go` (`//go:build windows`), com a mesma assinatura `aplicar(caminho string, novo []byte, perm os.FileMode) error`.

**Unix.** Escreve o binário novo num arquivo temporário **no mesmo diretório** do atual — para o rename não cruzar filesystem —, aplica a mesma permissão do original, `fsync`, e então `rename`. O inode antigo sobrevive enquanto o processo em execução o mantiver aberto.

**Windows.** O executável em execução não pode ser deletado, mas pode ser renomeado. A sequência:

1. Escreve o novo como `ngx.exe.new` no mesmo diretório.
2. Renomeia `ngx.exe` para `ngx.exe.old`.
3. Renomeia `ngx.exe.new` para `ngx.exe`.
4. **Tenta** remover o `.old`, e ignora a falha — ela é esperada, porque o arquivo ainda está em uso pelo processo que está rodando.

Se o passo 3 falhar depois de o 2 ter dado certo, restaure: renomeie o `.old` de volta. Deixar o usuário sem `ngx.exe` é o pior desfecho possível desta função, pior que não atualizar.

**Limpeza do `.old`.** Acrescente uma função `LimparResiduo(caminho string)` que remove um `.old` remanescente, e chame-a **na inicialização do `ngx`**, silenciosamente — se falhar, não é problema do usuário. Sem isso, cada atualização deixa um binário órfão no diretório para sempre.

Em ambos os casos, se faltar permissão de escrita no diretório, erro claro dizendo qual diretório e que privilégio é necessário, sem tentar escalar.

Teste os dois caminhos. O de Windows pode ser exercitado num runner `windows-latest` avulso, ou verificado à mão — mas registre no relatório o que foi de fato executado e o que não foi.

- [ ] **Step 6: Escrever o comando**

`internal/cli/update.go`, com as flags:

- `--check`: só reporta se há versão nova, sem baixar nada. Exit 0 se atualizado, exit 7 se há atualização pendente — reaproveitando o código de "mudanças pendentes" que a spec já define.
- `--channel stable|beta`: default `stable`.
- `--version vX.Y.Z`: instala versão específica, inclusive mais antiga.

A saída segue o envelope de sempre, com `data` trazendo `current_version`, `latest_version`, `channel` e `updated`.

- [ ] **Step 7: Rodar a suíte**

Run: `go test ./... -race`
Expected: tudo verde.

- [ ] **Step 8: Commit**

```bash
git add internal/update/ internal/cli/update.go internal/cli/root.go
git commit -m "feat(update): auto-atualizacao com verificacao de assinatura"
```

---

### Task D5: README de instalação e atualização

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: tudo acima
- Produces: documentação de instalação, atualização e verificação manual

- [ ] **Step 1: Escrever as seções**

Acrescente ao `README.md` seções cobrindo:

**Instalação em Linux e macOS** — o one-liner de `curl`, mostrando as **duas** formas desde o começo, porque a maioria das máquinas onde o `ngx` é útil exige privilégio para escrever em `/usr/local/bin`:

```sh
# instalacao no sistema (precisa de privilegio)
curl -fsSL https://raw.githubusercontent.com/eduardoborges/ngx/main/install.sh | sudo sh

# instalacao no seu usuario, sem privilegio
curl -fsSL https://raw.githubusercontent.com/eduardoborges/ngx/main/install.sh | NGX_INSTALL_DIR=$HOME/.local/bin sh
```

Documente `NGX_CHANNEL`, `NGX_VERSION` e `NGX_INSTALL_DIR`. Mostre também o download manual, para quem não executa script vindo da internet — e diga que essa é uma preferência legítima, não paranoia.

**Instalação no Windows** — o one-liner de PowerShell, o diretório padrão (`%LOCALAPPDATA%\ngx\bin`), o aviso de abrir um terminal novo para o `PATH` valer, e como instalar em local que exija administrador.

**Nota honesta sobre nginx no Windows** — o `ngx` roda em Windows, mas o nginx de lá é oficialmente **beta** segundo o próprio nginx.org: usa apenas `select()`/`poll()`, apenas um worker efetivamente trabalha, não há suporte a UDP nem QUIC, e faltam os módulos XSLT, image filter, GeoIP e Perl embutido. Diga isso sem rodeios e aponte para `https://nginx.org/en/docs/windows.html`.

Explique também que, no Windows, o nginx normalmente não é instalado por gerenciador de pacotes: fica num diretório desempacotado, tipo `C:\nginx-1.31.3\`, e usa o diretório de execução como prefixo. Então a autodetecção que funciona em Linux não se aplica, e é preciso apontar o caminho explicitamente:

```powershell
ngx inspect -c C:\nginx-1.31.3\conf\nginx.conf
```

**Atualização** — `ngx update`, `ngx update --check`, `ngx update --channel beta`. Explique que o update verifica assinatura minisign e checksum antes de substituir o binário, e que um binário compilado localmente recusa se auto-atualizar por não ter chave pública embutida.

No Windows, acrescente que o executável em uso não pode ser removido pelo sistema, então a versão antiga fica como `ngx.exe.old` até a próxima execução do `ngx`, que a apaga sozinha. Se a pessoa vir esse arquivo, é esperado — dizer isso evita o chamado de suporte.

Em Linux e macOS, se o `ngx` estiver instalado em diretório do sistema, o update precisa do mesmo privilégio da instalação: documente `sudo ngx update`.

**Canais** — que `v0.2.0` é stable e `v0.2.0-beta.1` é beta, que o canal beta recebe as duas, e que toda a série v0.x é instável por natureza independentemente do canal.

**Verificação manual** — os comandos exatos para conferir uma release à mão com `minisign -V` e `sha256sum -c`, com a chave pública do projeto no texto. Quem audita precisa disso sem ter que ler o código do updater.

Seja explícito sobre a diferença de garantia: o script de instalação verifica **checksum**; o `ngx update` verifica **assinatura e checksum**.

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: instalacao, atualizacao e verificacao de releases"
```

---

## Verificação de cobertura

| Pedido | Task |
|---|---|
| CI via GitHub Actions | D1 |
| Release na main | D2 (por tag, disparada a partir de `main`) |
| Instalação via curl | D3 (`install.sh`) |
| Instalação e atualização no Windows | D2, D3 (`install.ps1`), D4, D5 |
| Auto-update `ngx update` | D4 |
| Releases diferenciadas beta/stable | D2 (`prerelease: auto`) e D4 (`--channel`) |
| Documentação no README | D5 |
| Aviso de `sudo` na instalação | D3 (antes do download) e D5 |

## Ordem de execução

D1 é independente e pode ir primeiro. D2 precisa das chaves criadas fora do CI. D3 e D4 dependem de D2 ter publicado ao menos uma release para testar contra algo real — até lá, testam contra artefatos gerados por `goreleaser --snapshot`. D5 é o fechamento.

## O que este plano não cobre

Homebrew tap, pacotes `.deb`/`.rpm`, imagem Docker e publicação em registries. Todos são adições diretas ao `.goreleaser.yaml` depois que o fluxo básico estiver funcionando, e nenhum muda a arquitetura decidida aqui.
