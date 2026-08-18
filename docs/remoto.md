# Operando um host remoto

O `ngx` le e inspeciona a configuracao de um servidor remoto por SSH. Nada e
instalado do outro lado.

> **Estado.** O caminho remoto esta implementado e coberto por testes, mas
> **nao foi exercitado contra um servidor de producao por este projeto**. Os
> numeros de latencia citados no fim vem da medicao que originou o desenho, e
> nao de uma execucao do `ngx`. Trate esta pagina como documentacao de algo
> novo, nao de algo rodado.

## Uso minimo

```console
$ ngx --host web1 inspect
```

Se `ssh web1` ja funciona na sua maquina, isso funciona: o `ngx` le o mesmo
`~/.ssh/config`, usa o mesmo `ssh-agent` e confere o mesmo
`~/.ssh/known_hosts`. Sem flag nenhuma alem de `--host`.

`--host` aceita tanto um alias do `~/.ssh/config` quanto um endereco. Quando e
um alias, o `HostName` do arquivo traduz o alias para o alvo real — exatamente
como o `ssh` faz.

Do `~/.ssh/config` o `ngx` honra **quatro chaves**: `HostName`, `User`, `Port`
e `IdentityFile`. E so. `ProxyJump`, `Match`, `Include` e o resto nao sao
aplicados. Honrar pouco e dizer qual pouco e melhor que honrar mal em
silencio; se voce depende de `ProxyJump`, o `ngx` ainda nao serve para aquele
host.

Flags explicitas vencem o arquivo, que vence o default:

```console
$ ngx --host 10.0.0.7 --user deploy --port 2222 --key ~/.ssh/id_ed25519 inspect -c /etc/nginx/nginx.conf
```

Uma flag vazia nao e uma flag: `--user ""` nao apaga o `User` que veio do
arquivo. E qualquer flag de conexao sem `--host` e erro de uso, nao um valor
ignorado em silencio — quem digitou `--user deploy` acredita que a conexao vai
usar aquele usuario:

```console
$ ngx --user deploy version; echo "exit=$?"
{"ok":false,"command":"version","ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0002","message":"--user so faz sentido junto de --host"}],"meta":{"duration_ms":0}}
exit=2
```

## Nada e instalado no servidor

O `ngx` roda inteiro na sua maquina. Do lado remoto ele usa duas coisas que
qualquer servidor com OpenSSH ja tem:

- **SFTP**, para ler os arquivos de configuracao;
- **execucao de comando**, para rodar o `nginx` que **ja esta la**, com o argv
  explicito — nunca uma linha de shell montada por concatenacao.

Nao ha binario copiado, nao ha agente, nao ha diretorio criado, nao ha pacote
instalado. Desconectar nao deixa rastro. Isso e requisito de desenho, nao
consequencia: uma ferramenta que precisa se instalar em cada servidor para
poder ler um `.conf` nao seria usavel em maquina de cliente.

## Autenticacao

A ordem e sempre a mesma: **ssh-agent, depois chave em arquivo, depois
senha**. Todos os metodos disponiveis sao oferecidos ao servidor, que escolhe.

1. **`ssh-agent`.** Se houver um agente alcancavel (`SSH_AUTH_SOCK`, ou o
   named pipe padrao no Windows), as chaves dele entram primeiro. A lista e
   pedida ao agente no momento da autenticacao, entao uma chave adicionada com
   `ssh-add` depois que o `ngx` comecou ainda e vista.

   Nao ter `ssh-agent` **nao e erro** — e um metodo a menos, informado como
   diagnostico de severidade `info`.

2. **Chave em arquivo**, de `--key` ou do `IdentityFile` do `~/.ssh/config`.
   Se a chave for cifrada, a passphrase vem de `NGX_SSH_KEY_PASSPHRASE` ou de
   um prompt quando a entrada padrao e um terminal. Sob pipe, sem a variavel,
   o metodo simplesmente sai da lista com um aviso que nomeia a variavel — em
   vez de o comando travar esperando uma digitacao que nunca vem. E o que
   mantem o `ngx` utilizavel por um agente de IA.

3. **Senha**, de `NGX_SSH_PASSWORD` ou de um prompt sem eco no terminal.

### Nao existe flag de senha, e isso e deliberado

Nenhuma senha, nem passphrase, entra por linha de comando. **Nao ha
`--password`**, e adicionar uma deve ser reprovado em review.

O motivo e que o valor de uma flag nao fica onde voce o escreveu:

- aparece em `ps` para **qualquer usuario da maquina**, enquanto o processo
  vive;
- fica gravado no historico do shell, em texto puro, para sempre;
- vai inteiro para o log de qualquer CI, e logs de CI sao lidos por muito mais
  gente do que quem escreveu o pipeline.

Quem passa uma senha por flag ja a vazou, mesmo que o comando tenha
funcionado. As duas entradas aceitas — variavel de ambiente e prompt — nao tem
esse problema.

```console
$ NGX_SSH_PASSWORD='...' ngx --host web1 inspect
```

Mesmo a variavel de ambiente merece cuidado: prefira um gerenciador de
segredos que a injete no processo, e nao um `export` no `.bashrc`.

## Host desconhecido e recusado

O `ngx` confere a chave do servidor contra o `known_hosts` e **recusa a
conexao** quando nao tem com o que comparar. Nao ha "aceita e continua", nao
ha pergunta interativa que um script responda com `yes` por acidente.

Sao quatro desfechos, com codigos distintos, para o consumidor da saida poder
decidir sem interpretar texto:

| Codigo | Situacao |
|---|---|
| `NGX-0201` | host desconhecido — nunca visto antes |
| `NGX-0202` | **a chave do host mudou** — pode ser interceptacao |
| `NGX-0203` | a chave apresentada esta marcada `@revoked` |
| `NGX-0204` | o arquivo `known_hosts` nao existe |

O caso do arquivo ausente, real:

```console
$ ngx --host 127.0.0.1 --port 2222 --known-hosts /tmp/nao-existe-known-hosts --timeout 3s version; echo "exit=$?"
{"ok":false,"command":"version","ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"error","code":"NGX-0204","message":"/tmp/nao-existe-known-hosts nao existe: o ngx nao tem nenhuma chave registrada para comparar com a de 127.0.0.1. Rode `ssh 127.0.0.1` uma vez para registrar o host, aponte outro arquivo com --known-hosts, ou aceite qualquer chave com --insecure-host-key (inseguro)","file":"/tmp/nao-existe-known-hosts"}],"meta":{"duration_ms":0}}
exit=1
```

Host desconhecido (`NGX-0201`) e o atrito normal de primeiro acesso, e a
mensagem entrega **a linha pronta** para colar no `known_hosts`. Chave
alterada (`NGX-0202`) e outra coisa inteiramente: a mensagem diz "pode ser um
ataque de interceptacao" com todas as letras, mostra a chave apresentada ao
lado das registradas, e aponta arquivo e linha do registro que diverge.

### Adicionando um host ao `known_hosts`

Tres caminhos, do melhor para o pior:

```console
# 1. conectar uma vez pelo proprio ssh, conferindo o fingerprint na tela
$ ssh web1

# 2. buscar a chave e conferir o fingerprint contra o que o servidor publica
$ ssh-keyscan -H web1 >> ~/.ssh/known_hosts

# 3. colar a linha que a mensagem de erro NGX-0201 ja entregou pronta
```

O `ssh-keyscan` sozinho **nao verifica nada** — ele pergunta a chave a quem
atender na porta 22, que e exatamente quem um atacante estaria se passando por
ser. Ele so e seguro se voce comparar o fingerprint com uma fonte independente
(o console do provedor, o inventario, um colega no telefone).

Se a chave mudou legitimamente — servidor reinstalado —, remova a antiga com
`ssh-keygen -R web1` e registre a nova.

### `--insecure-host-key`

Aceita a chave do servidor **sem verificacao nenhuma**. A conexao continua
cifrada, mas deixa de estar protegida contra interceptacao: qualquer maquina
na rota pode se passar pelo servidor, e o `ngx` nao teria como notar.

```console
$ ngx --host 127.0.0.1 --port 2222 --insecure-host-key --timeout 3s version
{"ok":false,"command":"version","ngx_version":"0.1.0-dev","data":null,"diagnostics":[{"severity":"warning","code":"NGX-0211","message":"--insecure-host-key: a chave de host de 127.0.0.1 sera aceita sem nenhuma verificacao. A conexao nao esta protegida contra interceptacao (man-in-the-middle) e qualquer maquina na rota pode se passar pelo servidor"},{"severity":"error","code":"NGX-0206","message":"nao foi possivel conectar em eduardoborges@127.0.0.1:2222: dial tcp 127.0.0.1:2222: connect: connection refused. Metodos de autenticacao oferecidos: ssh-agent"}],"meta":{"duration_ms":0}}
```

O aviso `NGX-0211` sai na saida **toda vez**, junto com o resultado, no
sucesso e no erro. Ele nao pode ser suprimido, e essa e a intencao: um escape
de seguranca silencioso deixa de ser um escape e vira o comportamento normal.

**Nao deixe isso virar habito.** O uso legitimo e estreito: uma bancada
descartavel, um container de teste que ganha host key nova a cada `docker
run`, um laboratorio efemero. Se `--insecure-host-key` apareceu no seu script
de producao, ou no alias que voce digita todo dia, o problema nao e a
verificacao — e que o `known_hosts` da sua frota nao esta sendo gerenciado.
Distribuir um `known_hosts` da frota, ou usar um certificado de host assinado
por uma CA SSH, resolve de verdade.

## Privilegio: `--sudo` e explicito

Num servidor de producao, a configuracao do nginx costuma ser legivel so por
root, e o `sudo` costuma estar liberado sem senha. Ou seja: o caminho que
"simplesmente funciona" e o de escalar privilegio em silencio.

O `ngx` **nao faz isso**. Se um comando remoto precisa de privilegio, ele so
roda com `--sudo` explicito. Sem a flag, o `ngx` reporta que o comando exige
privilegio e **qual comando e** — nao tenta de novo com `sudo`, nao adivinha.

```console
$ ngx --host web1 --sudo test
```

Uma ferramenta dirigida por agente de IA que escala privilegio sozinha, num
servidor de producao, transforma um erro de leitura em um comando `root`. O
atrito de digitar `--sudo` e o registro de que **alguem decidiu**.

Detalhes que evitam surpresa:

- O `sudo` e invocado com `-n` (nao interativo), porque o `ngx` executa sem
  TTY. Um `sudo` que peca senha falha com diagnostico proprio
  (`NGX-0222`) em vez de travar esperando digitacao.
- `--sudo` vale tambem no alvo **local**. Privilegio explicito nao e uma regra
  do caminho remoto; e uma regra do `ngx`.
- **`--sudo` vale para quem executa o nginx, e nao para quem le arquivo.** Os
  comandos `test` e `status` executam o binario (`nginx -t`, `nginx -V`) e
  passam a rodar `sudo -n nginx ...` quando a flag esta presente. O `inspect`
  nao: ele le os arquivos por SFTP, e leitura por SFTP nao passa por `sudo` —
  ela acontece com as permissoes do usuario que conectou. Se
  `/etc/nginx/nginx.conf` nao for legivel por ele, o `inspect` falha ao abrir
  o arquivo, e `--sudo` nao ajuda. A saida a curto prazo e conectar com um
  usuario que tenha leitura.

## No Windows, habilite o servico `ssh-agent`

O Windows 10/11 e o Windows Server ja trazem o OpenSSH, mas o servico
`ssh-agent` vem **desabilitado de fabrica**. O `ngx` fala com o named pipe
`\\.\pipe\openssh-ssh-agent` — o mesmo que o `ssh.exe` usa — e, com o servico
parado, o pipe simplesmente nao existe: o `ngx` reporta o agente como
indisponivel (severidade `info`, nao erro) e segue para chave em arquivo e
senha.

Para habilitar, num PowerShell **como administrador**:

```powershell
Get-Service ssh-agent | Select-Object Name, Status, StartType
Set-Service -Name ssh-agent -StartupType Automatic
Start-Service ssh-agent
```

Depois, no seu terminal normal:

```powershell
ssh-add $env:USERPROFILE\.ssh\id_ed25519
ssh-add -l
```

Se `SSH_AUTH_SOCK` estiver definida — porque voce usa 1Password, `gpg-agent`
ou um relay do WSL —, ela tem precedencia sobre o pipe padrao. E a mesma
ordem que o OpenSSH do Windows aplica, entao um agente alternativo que ja
funciona com o `ssh.exe` funciona com o `ngx` sem configuracao.

## A ressalva de latencia

**Cada `include` da configuracao e uma leitura de rede.** Localmente uma
leitura de arquivo custa microssegundos e ninguem pensa nisso; por SSH, cada
uma paga a latencia do link.

Medido num nginx de producao real — Oracle Linux 9, nginx 1.20.1, acesso por
VPN — a configuracao efetiva tinha **132 arquivos**. Sao 132 leituras de rede
para montar uma arvore, e elas hoje acontecem **em sequencia**: nao ha
paralelismo na resolucao de includes. Num link de 50 ms de ida e volta, so o
round-trip ja soma mais de seis segundos, antes de qualquer byte de conteudo.

O que isso significa na pratica:

- `ngx --host ... inspect` numa configuracao grande **nao e interativo**.
  Ajuste o `--timeout` (default 30s) de acordo, e nao o coloque dentro de um
  laco.
- Uma configuracao pequena responde rapido; o custo cresce com o numero de
  arquivos, nao com o tamanho deles.
- Paralelizar a leitura por nivel da arvore e a otimizacao obvia e esta
  prevista. Ate ela existir, o numero acima e o que voce deve esperar.

Ha ainda um detalhe de correcao que a latencia esconde: o `ngx` implementa o
proprio glob remoto sobre `ReadDir`, em vez de usar o `Glob` do `pkg/sftp`,
porque aquele **descarta erros de I/O por contrato**. Com ele, um
`include /etc/nginx/conf.d/*.conf` num link instavel devolveria zero arquivos
e nenhum erro — e o `ngx` apresentaria a configuracao do servidor sem os
arquivos que ela tem, como se o servidor genuinamente nao os tivesse. Uma
ferramenta lida por agente de IA nao pode ser confiantemente incompleta: o
consumidor nao tem como desconfiar.

## O que ainda nao existe

- Multiplos hosts numa chamada (`--hosts a,b,c`).
- `ProxyJump`, bastion e tunel.
- Escrita transacional remota — depende dos comandos de mutacao da v0.2.
- Leitura paralela de includes.
- `ngx --host web1 version` **abre uma conexao SSH** para imprimir uma string
  puramente local. E lento e pode falhar por motivo alheio ao comando; esta
  registrado como limitacao conhecida.
