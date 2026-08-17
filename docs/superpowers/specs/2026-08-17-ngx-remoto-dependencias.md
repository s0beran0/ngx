# Dependências do acesso remoto por SSH

Este documento registra a investigação exigida pelo Step 1 da Task R2 do plano
`docs/superpowers/plans/2026-08-17-ngx-remoto-ssh.md`. Ele existe porque três
defeitos desta base nasceram de código de integração escrito de memória: aqui
toda afirmação tem fonte, e a fonte é o código da biblioteca ou o código do
OpenSSH, não a lembrança de quem escreveu.

Versões investigadas (as que o `go.mod` deve fixar):

| Módulo | Versão | Papel |
| --- | --- | --- |
| `golang.org/x/crypto` | v0.55.0 | `ssh`, `ssh/agent`, `ssh/knownhosts` |
| `github.com/pkg/sftp` | v1.13.11 | leitura e glob remotos |
| `github.com/kevinburke/ssh_config` | v1.6.0 | parse de `~/.ssh/config` |
| `github.com/Microsoft/go-winio` | v0.6.2 | named pipe do `ssh-agent` no Windows |

Lembrete de terminologia: `ssh-agent` é o programa do sistema operacional que
guarda chaves destravadas. Não tem relação com o *agente de IA* que consome a
saída do `ngx`.

## Prova de ausência de cgo

Ler o `go.mod` de uma dependência **não** é prova: `go.mod` não diz nada sobre
arquivos `import "C"` escondidos atrás de build tags. A prova é compilar.

Um módulo descartável importando as quatro bibliotecas — inclusive um arquivo
com build tag `windows` que chama `winio.DialPipe` — foi compilado com:

```
CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build ./...
```

Resultado, com Go 1.25.9:

| Plataforma | Resultado |
| --- | --- |
| linux/amd64 | OK |
| linux/arm64 | OK |
| darwin/amd64 | OK |
| darwin/arm64 | OK |
| windows/amd64 | OK |
| windows/arm64 | OK |

Grafo de dependências não-stdlib do build de `windows/amd64`: `x/crypto/ssh`
(+ `blowfish`, `chacha20`, `cryptobyte`, `curve25519`, `poly1305`,
`bcrypt_pbkdf`), `pkg/sftp` (+ `kr/fs`), `kevinburke/ssh_config`,
`Microsoft/go-winio` (+ `internal/fs`, `internal/socket`, `internal/stringbuffer`,
`pkg/guid`) e `golang.org/x/sys/windows`. Nada mais.

Sobre o `go-winio`: o `go.mod` dele declara `github.com/sirupsen/logrus`,
`golang.org/x/tools` e `golang.org/x/mod`. Nenhum dos três entra no build —
são dependências de teste e de ferramentas. O `go mod tidy` do módulo de prova
fechou com apenas dois `indirect`: `github.com/kr/fs` e `golang.org/x/sys`.
`x/sys` o `ngx` já usa.

**Conclusão: nenhuma das candidatas exige cgo.** O binário estático continua
possível nas seis plataformas.

## 1. `ssh-agent` no Windows

O `ssh-agent` do Windows não é um socket Unix: é um **named pipe**. O caminho é

```
\\.\pipe\openssh-ssh-agent
```

Fonte, lado servidor — o próprio `ssh-agent.exe` cria o pipe com esse nome
literal:

- `PowerShell/openssh-portable`, branch `latestw_all`,
  `contrib/win32/win32compat/ssh-agent/agent.c:50`:
  `#define AGENT_PIPE_ID L"\\\\.\\pipe\\openssh-ssh-agent"`, usado no
  `CreateNamedPipeW` da linha 119-120.

Fonte, lado cliente — o `ssh.exe` do Windows **não** tem o caminho embutido no
código de autenticação; ele preenche `SSH_AUTH_SOCK` com esse valor no `wmain`
quando a variável está vazia, e daí para frente o código portável usa a
variável normalmente:

- `contrib/win32/win32compat/wmain_common.c:53-54`:
  ```c
  if (getenv("SSH_AUTH_SOCK") == NULL)
          _putenv("SSH_AUTH_SOCK=\\\\.\\pipe\\openssh-ssh-agent");
  ```
- `authfd.c:122-134` (`ssh_get_authentication_socket`) lê
  `SSH_AUTHSOCKET_ENV_NAME`, definido como `"SSH_AUTH_SOCK"` em `ssh.h:58`, e
  devolve `SSH_ERR_AGENT_NOT_PRESENT` se estiver vazio.
- No Windows, o `connect(AF_UNIX)` de `authfd.c` cai na camada de compatibilidade
  `contrib/win32/win32compat/fileio.c:122-153` (`fileio_connect`), que abre o
  caminho com `CreateFileW(..., OPEN_EXISTING, FILE_FLAG_OVERLAPPED | ...)` e
  faz retry enquanto o erro for `ERROR_PIPE_BUSY`.

**Regra que o `ngx` adota**, espelhando o OpenSSH: honrar `SSH_AUTH_SOCK` se
estiver definido — em qualquer plataforma — e, só no Windows e só quando ela
estiver vazia, cair no padrão `\\.\pipe\openssh-ssh-agent`. Isso faz o `ngx`
funcionar com o `ssh-agent` nativo sem configuração e, ao mesmo tempo, respeitar
quem aponta `SSH_AUTH_SOCK` para outro agente (1Password, gpg-agent, um relay
de WSL).

### Como conectar em Go

Duas rotas, ambas comprovadamente sem cgo:

**A — `github.com/Microsoft/go-winio` (recomendada).**
`func DialPipe(path string, timeout *time.Duration) (net.Conn, error)`
(`pipe.go:237`); há também `DialPipeContext` (`pipe.go:255`). O `net.Conn`
retornado satisfaz `io.ReadWriter`, que é o que `agent.NewClient` pede. A
biblioteca trata `ERROR_PIPE_BUSY` com espera, como o OpenSSH faz.

**B — `os.OpenFile` da biblioteca padrão.** Abrir o pipe como arquivo é
literalmente o que o `fileio_connect` do OpenSSH faz, e `*os.File` também é um
`io.ReadWriter`. Zero dependência nova.

Escolha: **A**. O custo real da dependência é baixo (nenhum módulo novo além do
próprio `go-winio`; `x/sys` já está no projeto) e ela cobre o retry de
`ERROR_PIPE_BUSY` e a semântica de overlapped I/O que a rota B deixaria para
nós reimplementar às cegas — nenhum dos dois é testável nesta base, que não tem
Windows no loop de desenvolvimento. Preferir o caminho já exercitado por
terceiros é a decisão certa justamente onde não conseguimos testar.

Ausência de `ssh-agent` **não é erro**: é só um método de autenticação que não
entra na lista.

## 2. `golang.org/x/crypto/ssh/agent`

- `func NewClient(rw io.ReadWriter) ExtendedAgent` — `ssh/agent/client.go:351`.
  Se o `rw` também for `io.Closer`, o cliente usa pipelining; caso contrário
  serializa as chamadas. Um `net.Conn` (Unix ou named pipe) satisfaz os dois.
- O cliente vira `ssh.AuthMethod` por meio dos signers:
  `func (c *client) Signers() ([]ssh.Signer, error)` — `ssh/agent/client.go:819` —
  combinado com `func PublicKeysCallback(getSigners func() ([]Signer, error)) AuthMethod`
  — `ssh/client_auth.go:492`.

Ou seja: `ssh.PublicKeysCallback(agent.NewClient(conn).Signers)`. Usar
`PublicKeysCallback` em vez de `ssh.PublicKeys(signers...)` (`client_auth.go:486`)
importa: a lista de chaves é buscada no momento da autenticação, então uma chave
adicionada ao `ssh-agent` depois de o `ngx` iniciar ainda é vista.

Os demais métodos da ordem de autenticação:
`ssh.ParsePrivateKey` (`ssh/keys.go:1314`),
`ssh.ParsePrivateKeyWithPassphrase` (`ssh/keys.go:1326`),
`ssh.Password` (`ssh/client_auth.go:228`) e
`ssh.PasswordCallback` (`ssh/client_auth.go:234`).

## 3. `golang.org/x/crypto/ssh/knownhosts` — e a diferença entre os dois erros

`func New(files ...string) (ssh.HostKeyCallback, error)` — `knownhosts.go:417`.
Ele abre cada arquivo com `os.Open`; **arquivo inexistente devolve erro na
construção**, não no callback. Verificado: o erro é um `*fs.PathError`
(`open ...: no such file or directory`). O `ngx` precisa tratar "usuário nunca
usou ssh nesta máquina" como um caso próprio, com mensagem própria, e não
deixar vazar um `PathError` cru.

A distinção que a Task R2 exige está em um único tipo, discriminado por um
campo — não por dois tipos diferentes:

```go
// knownhosts.go:317-330
type KeyError struct {
        // Want holds the accepted host keys. For each key algorithm,
        // there can be multiple hostkeys.  If Want is empty, the host
        // is unknown. If Want is non-empty, there was a mismatch, which
        // can signify a MITM attack.
        Want []KnownKey
}

func (u *KeyError) Error() string {
        if len(u.Want) == 0 {
                return "knownhosts: key is unknown"
        }
        return "knownhosts: key mismatch"
}
```

Portanto:

| Situação | Como detectar | Mensagem do `ngx` |
| --- | --- | --- |
| Host desconhecido | `errors.As(err, &ke)` e `len(ke.Want) == 0` | atrito normal: diga o host e a linha a acrescentar |
| **Chave alterada** | `errors.As(err, &ke)` e `len(ke.Want) > 0` | **possível ataque**: diga isso com todas as letras, e mostre `ke.Want[i]` |
| Chave revogada | `errors.As(err, &re)` com `*knownhosts.RevokedError` (`knownhosts.go:333-339`) | recusa, informando o arquivo e a linha |

O erro é sempre devolvido por ponteiro (`&KeyError{}` em `knownhosts.go:375` e
`&RevokedError{...}` em `knownhosts.go:345`), então o `errors.As` precisa de
`**KeyError` — isto é, uma variável `var ke *knownhosts.KeyError`.

Como testar, sem rede: montar um `known_hosts` em `t.TempDir()`, chamar
`knownhosts.New`, e invocar o `ssh.HostKeyCallback` retornado diretamente,
passando um `*net.TCPAddr` como `remote` e uma `ssh.PublicKey` obtida de
`ssh.ParseAuthorizedKey`. Foi exatamente assim que os três casos acima foram
confirmados; a saída observada foi `"knownhosts: key mismatch"` com
`Want[0] = "<arquivo>:1: ssh-ed25519 AAAA..."` para chave trocada, e
`"knownhosts: key is unknown"` com `Want` vazio para host ausente.

Para a mensagem de host desconhecido, `knownhosts` fornece as duas peças da
linha que o usuário precisa colar:
`func Normalize(address string) string` (`knownhosts.go:441`) e
`func Line(addresses []string, key ssh.PublicKey) string` (`knownhosts.go:461`).
`Line([]string{Normalize("10.0.0.9:22")}, key)` produz
`10.0.0.9 ssh-ed25519 AAAA...` — a porta 22 é omitida, portas não padrão viram
`[host]:porta`.

## 4. `github.com/pkg/sftp`

Go puro (confirmado pelo build cruzado da tabela acima; `go.mod` declara apenas
`kr/fs`, `x/crypto`, `x/sys` e o testify de teste).

- Cliente: `func NewClient(conn *ssh.Client, opts ...ClientOption) (*Client, error)`
  — `client.go:197`.
- Leitura: `func (c *Client) Open(path string) (*File, error)` — `client.go:657`.
  Abre com `O_RDONLY`; o `*File` implementa `io.Reader`, que é o que o
  `ParseOptions.Open` da v0.1 precisa.
- **Sim, tem glob**: `func (c *Client) Glob(pattern string) ([]string, error)`
  — `match.go:43`. A sintaxe é a de `path.Match`, a mesma de `filepath.Glob`,
  e portanto a mesma que o crossplane já espera do `Glob` injetado na Task R3.

Duas ressalvas do fonte que precisam virar comportamento consciente:

- `Glob` **ignora erros de sistema de arquivos**, inclusive I/O ao ler
  diretórios (`match.go:40-42`). O único erro possível é `ErrBadPattern`. Um
  diretório sem permissão de leitura no servidor produz, silenciosamente, uma
  lista mais curta — não um erro. Se um `include` remoto casar com zero
  arquivos, o `ngx` não consegue distinguir "não existe" de "não pude ler".
- Padrão sem metacaractere vira um `Lstat`, e um `Lstat` que falha devolve
  `nil, nil` (`match.go:44-48`) — de novo, ausência silenciosa.

## 5. Parser de `~/.ssh/config`

`github.com/kevinburke/ssh_config` v1.6.0 é Go puro e **não tem nenhuma
dependência** (o `go.mod` dele tem só as linhas `module` e `go 1.18`).

O README do projeto afirma que "the `Match` directive is currently unsupported".
**O README está desatualizado**: o fonte da v1.6.0 implementa um subconjunto de
`Match`. O que vale é o `parser.go`, verificado empiricamente.

### O que é honrado

| Recurso | Situação | Fonte |
| --- | --- | --- |
| `Host` com wildcard (`web*`, `?`) | sim | `config.go:491` (`NewPattern`), `config.go:550` (`Matches`) |
| `Host` com negação (`!web9`) | sim — casar com um padrão negado descarta o bloco inteiro | `config.go:550-568` |
| `Include`, inclusive com wildcard no caminho | sim, até 5 níveis de recursão (`ErrDepthExceeded`) | `config.go:705-795` |
| `Match all` | sim, tratado como `Host *` | `parser.go:184-195` |
| `Match Host <padrões>` | sim | `parser.go:196-222` |
| `IdentityFile` múltiplo | sim, via `GetAll` | `config.go:406` |
| Chaves case-insensitive | sim | `config.go:376` |

### O que não é honrado — e como falha

`Match exec` é **rejeitado de propósito**, com um comentário no fonte dizendo
por quê: executar comando arbitrário deixaria um `~/.ssh/config` não confiável
rodar código na máquina de quem faz o parse (`parser.go:224-230`). Isso está
alinhado com a política do `ngx` de não fazer `exec` de shell; a decisão da
biblioteca é a nossa decisão.

Qualquer outro critério de `Match` — `user`, `localuser`, `final`, `canonical`,
`originalhost`, `exec` — produz **erro de parse do arquivo inteiro**, não uma
diretiva ignorada. Mensagens observadas:

```
(1, 7): ssh_config: Match Exec is not supported
(1, 7): ssh_config: unsupported Match criterion "user"
(1, 7): ssh_config: unsupported Match criterion "final"
(1, 7): ssh_config: unsupported Match criterion "canonical"
(1, 7): ssh_config: unsupported Match criterion "originalhost"
(1, 7): ssh_config: unsupported Match criterion "localuser"
```

Essa é a consequência prática mais importante desta investigação: **um usuário
com `Match final` ou `Match exec` no `~/.ssh/config` faz o `ngx` falhar a
leitura do arquivo todo, não só daquele bloco.**

### Decisão

O `ngx` honra: `Host` (com wildcard e negação), `Include`, `Match all` e
`Match Host`. As diretivas lidas são `HostName`, `User`, `Port` e
`IdentityFile`. Nada mais do `ssh_config` influencia o comportamento — em
particular `ProxyJump`, `ProxyCommand`, `ControlMaster` e `IdentityAgent`
**não** são honrados, e não são honrados em silêncio: quem depende deles precisa
saber que o `ngx` os ignora.

E quando o parse falha por um `Match` não suportado, o `ngx` **não** trata isso
como "arquivo ausente". Ele emite um diagnóstico dizendo qual critério não é
suportado, que a resolução por `~/.ssh/config` foi pulada, e que as flags
explícitas (`--host`, `--user`, `--port`, `--key`) continuam funcionando. Falhar
com "não achei o host" quando na verdade não conseguimos ler o arquivo seria
mentir sobre a causa.

### Armadilhas de API

- Use `ssh_config.Decode(io.Reader)` (`config.go:329`) e os métodos de
  `*Config`. **`(*Config).Get` não aplica valores default** — `config.go:375-402`
  termina em `return "", nil`. Defaults só saem por `UserSettings.Get` /
  `GetStrict`, que consultam `Default(keyword)` (`validators.go:14`). Verificado:
  `cfg.Get("host-nao-listado", "Port")` devolve `""`, não `"22"`. O `ngx` aplica
  os próprios defaults (porta 22, usuário corrente) depois do parse.
- `UserSettings` guarda o resultado num `sync.Once` (`config.go:271`). Em teste,
  crie um `&ssh_config.UserSettings{}` novo por caso e chame `ConfigFinder`
  antes do primeiro `Get`, senão o cache do caso anterior vaza.
- `Include` com caminho relativo é resolvido contra `~/.ssh` (ou `/etc/ssh`, se
  o arquivo é de sistema) usando um `homedir()` próprio da biblioteca
  (`config.go:63-70`), que chama `os/user.Current()` e cai em `$HOME` se isso
  falhar. Isso **não** é `os.UserHomeDir()`, então as duas resoluções podem
  divergir num ambiente exótico. Não é motivo para trocar de biblioteca; é
  motivo para o `ngx` não presumir que os dois caminhos são o mesmo.

## 6. Caminho do `~/.ssh` nas três plataformas

`os.UserHomeDir()` é adequado. O fonte do Go 1.25.9
(`$GOROOT/src/os/file.go:605-624`) escolhe a variável de ambiente por
`runtime.GOOS`:

```go
env, enverr := "HOME", "$HOME"
switch runtime.GOOS {
case "windows":
        env, enverr = "USERPROFILE", "%userprofile%"
case "plan9":
        env, enverr = "home", "$home"
}
if v := Getenv(env); v != "" {
        return v, nil
}
```

No Windows ele lê `%USERPROFILE%`, que é onde o OpenSSH do Windows procura o
`.ssh` — e, importante para nós, isso **não** depende de cgo nem de
`os/user.Current()`. Em Linux e macOS lê `$HOME`.

O caminho se monta com `filepath.Join(home, ".ssh", "known_hosts")`:
`filepath` usa o separador nativo, então o mesmo código produz
`/home/x/.ssh/known_hosts` e `C:\Users\x\.ssh\known_hosts`. Nunca concatenar
com `/` na mão.

Erro possível: `os.UserHomeDir()` devolve erro se a variável estiver vazia. Um
serviço de CI no Windows sem `%USERPROFILE%` cai nesse caso; a mensagem do
`ngx` deve dizer que não achou o diretório do usuário e que
`--known-hosts` / `--key` resolvem, em vez de propagar `"%userprofile% is not
defined"`.

## Resumo das decisões

1. `ssh-agent`: `SSH_AUTH_SOCK` primeiro em toda plataforma; no Windows, fallback
   para `\\.\pipe\openssh-ssh-agent` via `go-winio`. Agente ausente não é erro.
2. Autenticação: `ssh.PublicKeysCallback(agent.NewClient(conn).Signers)`, depois
   chave em arquivo, depois senha (de `NGX_SSH_PASSWORD` ou de prompt em
   terminal — nunca de flag).
3. Host key: `knownhosts.New`, com `*KeyError` classificado por `len(Want)` —
   vazio é host desconhecido, não vazio é **chave alterada e possível ataque**.
   `*RevokedError` é um terceiro caso. Arquivo ausente é um quarto.
4. Leitura e glob remotos: `sftp.Client.Open` e `sftp.Client.Glob`, cientes de
   que o `Glob` engole erros de I/O.
5. `~/.ssh/config`: `kevinburke/ssh_config` v1.6.0, honrando `Host` (wildcard e
   negação), `Include`, `Match all` e `Match Host`, e lendo `HostName`, `User`,
   `Port` e `IdentityFile`. Parse que falha por `Match` não suportado vira
   diagnóstico explícito, não silêncio.
6. `~/.ssh`: `os.UserHomeDir()` + `filepath.Join`.

Nenhum item exige cgo.
