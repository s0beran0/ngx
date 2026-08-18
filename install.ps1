<#
    Instalador do ngx para Windows.

        irm https://raw.githubusercontent.com/s0beran0/ngx/main/install.ps1 | iex

    Equivalente do install.sh, com as diferencas que a plataforma impoe:
    baixa .zip em vez de .tar.gz, instala em %LOCALAPPDATA%\ngx\bin (gravavel
    sem elevacao, ao contrario de /usr/local/bin no Unix) e acrescenta o
    diretorio ao PATH do usuario.

    Ordem deliberada das etapas: tudo que pode falhar sem rede falha ANTES do
    primeiro download — arquitetura, diretorio, permissao de escrita e
    ferramentas de verificacao.

    Este arquivo e escrito para ser executado tambem via "irm | iex", entao
    nao usa #Requires (que so vale para arquivo em disco) nem
    $MyInvocation.MyCommand.Path (que e nulo nesse modo).
#>

param(
    [switch] $Help
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
# O medidor de progresso do Invoke-WebRequest custa mais que o download em si
# no PowerShell 5.1, e polui a saida de quem esta lendo o resultado.
$ProgressPreference = 'SilentlyContinue'

$Repositorio  = 's0beran0/ngx'
$UrlApi       = "https://api.github.com/repos/$Repositorio"
$UrlReleases  = "https://github.com/$Repositorio/releases"

# ---------------------------------------------------------------------------
# PLACEHOLDER: CHAVE PUBLICA MINISIGN (DD2/DD3)
# ---------------------------------------------------------------------------
# A chave publica do projeto AINDA NAO FOI GERADA (Task D2). O valor abaixo e
# um placeholder proposital e NAO e uma chave: chave minisign real e uma linha
# base64 de 56 caracteres comecando por "RW". O texto foi escrito para ser
# impossivel de confundir com uma chave de verdade — um valor plausivel
# passaria despercebido em revisao e chegaria a producao verificando nada.
#
# Ao gerar a chave, substitua a linha abaixo pela linha de chave do arquivo
# ngx-minisign.pub (a segunda linha, sem o "untrusted comment:").
#
# Enquanto o placeholder estiver aqui, o script RECUSA instalar: ausencia de
# verificacao e falha, nunca um "segui em frente".
$ChavePublicaMinisign = 'PLACEHOLDER-CHAVE-MINISIGN-NAO-GERADA-VER-TASK-D2'
$PlaceholderChave     = 'PLACEHOLDER-CHAVE-MINISIGN-NAO-GERADA-VER-TASK-D2'

function Show-Ajuda {
    @'
install.ps1 - instalador do ngx para Windows

USO
  irm https://raw.githubusercontent.com/s0beran0/ngx/main/install.ps1 | iex
  .\install.ps1 [-Help]

VARIAVEIS DE AMBIENTE
  NGX_INSTALL_DIR       Diretorio de instalacao.
                        Default: %LOCALAPPDATA%\ngx\bin
                        Um diretorio como C:\Program Files exige PowerShell
                        como administrador; o script detecta antes de baixar
                        e diz o que fazer, sem tentar elevar sozinho.
  NGX_CHANNEL           stable (default) ou beta. beta inclui pre-lancamentos.
  NGX_VERSION           Versao fixa, ex: v0.2.0. Quando definida, a API do
                        GitHub nao e consultada.
  NGX_ALLOW_UNVERIFIED  Se 1, permite instalar quando a assinatura minisign
                        nao PODE ser verificada (minisign ausente ou chave
                        publica ainda nao gerada). NAO ignora assinatura
                        invalida nem checksum divergente: esses abortam
                        sempre, sem excecao.

EXEMPLOS
  $env:NGX_VERSION='v0.2.0'; irm https://raw.githubusercontent.com/s0beran0/ngx/main/install.ps1 | iex
  $env:NGX_INSTALL_DIR='D:\ferramentas\bin'; .\install.ps1

VERIFICACAO
  O checksum SHA256 e conferido sempre e nao tem como ser desligado.
  A assinatura minisign do checksums.txt e conferida quando o minisign esta
  instalado e a chave publica do projeto esta embutida neste script.
'@ | Write-Host
}

function Escreve-Linha {
    param([string] $Texto = '')
    # Write-Host e deliberado: a saida deste script e para uma pessoa lendo o
    # terminal, e o pipeline nao deve carregar texto de diagnostico.
    Write-Host $Texto
}

# Aborta com "throw", nao com "exit". No fluxo documentado — irm | iex — o
# script roda no escopo da sessao interativa, e ali "exit" encerra o proprio
# PowerShell: a janela fecha e a pessoa nunca le a mensagem que acabou de ser
# impressa. O throw interrompe a execucao, mantem a sessao viva e, quando o
# script e chamado por arquivo (powershell -File), ainda produz codigo de
# saida diferente de zero para automacao.
function Falha {
    param(
        [string]   $Mensagem,
        [string[]] $Detalhes = @()
    )
    Write-Host "erro: $Mensagem" -ForegroundColor Red
    foreach ($linha in $Detalhes) { Write-Host $linha }
    throw "instalacao abortada: $Mensagem"
}

if ($Help) {
    Show-Ajuda
    return
}

if ($PSVersionTable.PSVersion.Major -lt 5) {
    Falha "este script exige PowerShell 5.1 ou mais novo (encontrado $($PSVersionTable.PSVersion))" @(
        '',
        'o Windows 10 e o Windows Server 2016 ja trazem a versao 5.1.',
        'em versoes anteriores, instale o Windows Management Framework 5.1 ou',
        'o PowerShell 7: https://aka.ms/powershell'
    )
}

# O PowerShell 5.1 negocia TLS 1.0 por padrao em algumas instalacoes, e o
# GitHub recusa. No PowerShell 7 o default ja e adequado e mexer nisso e
# desnecessario.
if ($PSVersionTable.PSVersion.Major -lt 6) {
    try {
        [Net.ServicePointManager]::SecurityProtocol =
            [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    } catch {
        # Se a plataforma nao expuser Tls12, o download falha adiante com uma
        # mensagem propria; nao ha o que fazer aqui.
    }
}

# ---------------------------------------------------------------------------
# Etapa 1 - arquitetura
# ---------------------------------------------------------------------------

function Get-Arquitetura {
    # PROCESSOR_ARCHITECTURE reporta x86 quando um PowerShell 32 bits roda num
    # Windows 64 bits; nesse caso a arquitetura real esta em
    # PROCESSOR_ARCHITEW6432. Ignorar isso instalaria o binario errado.
    $arquitetura = $env:PROCESSOR_ARCHITEW6432
    if ([string]::IsNullOrEmpty($arquitetura)) {
        $arquitetura = $env:PROCESSOR_ARCHITECTURE
    }

    switch ($arquitetura) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default {
            Falha "arquitetura nao suportada: $arquitetura" @(
                '',
                'o ngx publica binarios para amd64 (x64) e arm64.',
                'para outras arquiteturas, compile do fonte:',
                "  git clone https://github.com/$Repositorio.git; cd ngx; go build ./cmd/ngx"
            )
        }
    }
}

# ---------------------------------------------------------------------------
# Etapa 2 - diretorio de instalacao e permissao
# ---------------------------------------------------------------------------

function Falha-DePrivilegio {
    param([string] $Motivo, [string] $Diretorio)

    Falha $Motivo @(
        '',
        'abra o PowerShell como administrador e rode a instalacao de novo:',
        '  clique com o botao direito no PowerShell > "Executar como administrador"',
        '',
        'ou deixe NGX_INSTALL_DIR indefinida: o default e %LOCALAPPDATA%\ngx\bin,',
        'que e gravavel sem elevacao. para limpar a variavel nesta sessao:',
        "  Remove-Item Env:NGX_INSTALL_DIR",
        '',
        "o diretorio pedido foi: $Diretorio",
        '',
        'este script nao tenta elevar privilegio sozinho: quem decide elevar e',
        'voce, com o comando na frente dos olhos.'
    )
}

function Prepara-Diretorio {
    $diretorio = $env:NGX_INSTALL_DIR

    if ([string]::IsNullOrEmpty($diretorio)) {
        if ([string]::IsNullOrEmpty($env:LOCALAPPDATA)) {
            Falha 'LOCALAPPDATA nao esta definida e NGX_INSTALL_DIR tambem nao' @(
                '',
                'aponte o diretorio explicitamente:',
                "  `$env:NGX_INSTALL_DIR='C:\ngx\bin'"
            )
        }
        $diretorio = Join-Path $env:LOCALAPPDATA 'ngx\bin'
    }

    if (Test-Path -LiteralPath $diretorio -PathType Leaf) {
        Falha "$diretorio existe e nao e um diretorio"
    }

    if (-not (Test-Path -LiteralPath $diretorio)) {
        try {
            New-Item -ItemType Directory -Path $diretorio -Force | Out-Null
        } catch {
            Falha-DePrivilegio "nao foi possivel criar o diretorio $diretorio" $diretorio
        }
    }

    # A escrita real e o unico teste que nao mente: Test-Path nao diz nada
    # sobre permissao, e o ACL efetivo de um diretorio protegido so aparece na
    # hora de gravar.
    $arquivoDeTeste = Join-Path $diretorio ".ngx-teste-de-escrita-$PID"
    try {
        [System.IO.File]::WriteAllText($arquivoDeTeste, 'ngx')
        Remove-Item -LiteralPath $arquivoDeTeste -Force
    } catch {
        Falha-DePrivilegio "sem permissao de escrita em $diretorio" $diretorio
    }

    return $diretorio
}

# ---------------------------------------------------------------------------
# Etapa 3 - verificacao (antes de baixar)
# ---------------------------------------------------------------------------

function Existe-Comando {
    param([string] $Nome)
    return [bool] (Get-Command $Nome -ErrorAction SilentlyContinue)
}

# Tres desfechos, nenhum silencioso: da para verificar; nao da e nao ha
# autorizacao (aborta); nao da e ha autorizacao explicita (segue com aviso).
function Avalia-VerificacaoDeAssinatura {
    $motivo = ''

    if ($ChavePublicaMinisign -eq $PlaceholderChave) {
        $motivo = 'a chave publica minisign do projeto ainda nao foi gerada e este script carrega um placeholder'
    } elseif (-not (Existe-Comando 'minisign')) {
        $motivo = 'o minisign nao esta instalado nesta maquina'
    }

    if ($motivo -eq '') {
        return $true
    }

    if ($env:NGX_ALLOW_UNVERIFIED -eq '1') {
        Escreve-Linha ''
        Write-Host '############################################################' -ForegroundColor Yellow
        Write-Host '# AVISO: INSTALANDO SEM VERIFICAR A ASSINATURA'              -ForegroundColor Yellow
        Write-Host '#'                                                          -ForegroundColor Yellow
        Write-Host "# $motivo."                                                 -ForegroundColor Yellow
        Write-Host '#'                                                          -ForegroundColor Yellow
        Write-Host '# NGX_ALLOW_UNVERIFIED=1 esta definida, entao a instalacao'  -ForegroundColor Yellow
        Write-Host '# segue. O checksum SHA256 ainda sera conferido, mas ele so' -ForegroundColor Yellow
        Write-Host '# protege contra download corrompido: nao protege contra um' -ForegroundColor Yellow
        Write-Host '# release publicado por quem tenha comprometido a conta do'  -ForegroundColor Yellow
        Write-Host '# GitHub, porque nesse caso o checksum viria adulterado.'    -ForegroundColor Yellow
        Write-Host '############################################################' -ForegroundColor Yellow
        Escreve-Linha ''
        return $false
    }

    $detalhes = @(
        '',
        "motivo: $motivo.",
        '',
        'o ngx opera a configuracao de um servidor que serve trafego. instalar',
        'um binario sem verificar de onde ele veio nao e um detalhe de higiene.',
        'por isso o script para aqui em vez de seguir em frente.',
        '',
        'como resolver:'
    )

    if ($ChavePublicaMinisign -eq $PlaceholderChave) {
        $detalhes += @(
            '  a chave publica ainda nao existe - nao ha o que instalar do seu',
            "  lado. acompanhe $UrlReleases e use uma versao deste script",
            '  publicada depois da primeira release assinada.'
        )
    } else {
        $detalhes += @(
            '  instale o minisign e rode de novo:',
            '    winget install jedisct1.minisign',
            '    ou baixe de https://github.com/jedisct1/minisign/releases'
        )
    }

    $detalhes += @(
        '',
        'se voce aceita o risco de forma consciente, e so nesse caso:',
        "  `$env:NGX_ALLOW_UNVERIFIED='1'"
    )

    Falha 'a assinatura do release nao pode ser verificada' $detalhes
}

# ---------------------------------------------------------------------------
# Etapa 4 - rede
# ---------------------------------------------------------------------------

# O tipo da excecao muda entre PowerShell 5.1 (WebException) e 7
# (HttpRequestException), e o codigo HTTP fica em lugares diferentes. Esta
# funcao devolve 0 quando nao foi possivel determinar.
function Get-CodigoHttp {
    param($Erro)

    try {
        $resposta = $Erro.Exception.Response
        if ($null -eq $resposta) { return 0 }

        $status = $resposta.StatusCode
        if ($null -eq $status) { return 0 }

        return [int] $status
    } catch {
        return 0
    }
}

function Falha-DeRelease {
    param([int] $Codigo, [string] $Onde, [string] $Versao = '')

    switch ($Codigo) {
        404 {
            $detalhes = @(
                '',
                'as duas causas possiveis:',
                '  1. o projeto ainda nao publicou nenhuma release. confira em',
                "     $UrlReleases"
            )
            if ($Versao -ne '') {
                $detalhes += @(
                    "  2. a versao pedida, $Versao, nao existe. o nome da tag inclui",
                    '     o "v" inicial: v0.1.0, nao 0.1.0.'
                )
            } else {
                $detalhes += @(
                    '  2. so existem pre-lancamentos. tente o canal beta:',
                    "     `$env:NGX_CHANNEL='beta'"
                )
            }
            Falha "nenhuma release encontrada para $Repositorio ($Onde respondeu 404)" $detalhes
        }
        403 {
            Falha "a API do GitHub recusou a consulta (HTTP 403) - provavel limite de requisicoes por IP" @(
                '',
                'o limite anonimo e por hora e por endereco. duas saidas:',
                '  - espere e tente de novo, ou',
                '  - fixe a versao, que dispensa a consulta a API:',
                "      `$env:NGX_VERSION='v0.1.0'"
            )
        }
        429 {
            Falha "a API do GitHub recusou a consulta (HTTP 429) - limite de requisicoes" @(
                '',
                'espere alguns minutos, ou fixe a versao para dispensar a API:',
                "  `$env:NGX_VERSION='v0.1.0'"
            )
        }
        0 {
            Falha "nao foi possivel falar com $Onde" @(
                '',
                'verifique a conexao de rede, o DNS e se ha proxy exigindo',
                'configuracao. nenhum arquivo foi escrito.'
            )
        }
        default {
            Falha "resposta inesperada de ${Onde}: HTTP $Codigo" @(
                '',
                'confira o estado do servico em https://www.githubstatus.com'
            )
        }
    }
}

function Baixa-Arquivo {
    param([string] $Url, [string] $Destino)

    try {
        Invoke-WebRequest -Uri $Url -OutFile $Destino -UseBasicParsing -ErrorAction Stop
        return 200
    } catch {
        return (Get-CodigoHttp $_)
    }
}

# Set-StrictMode transforma o acesso a uma propriedade inexistente em erro de
# execucao. Uma resposta da API fora do formato esperado explodiria com um
# stack trace do PowerShell em vez da mensagem util deste script.
function Get-PropriedadeTexto {
    param($Objeto, [string] $Nome)

    if ($null -eq $Objeto) { return '' }
    $propriedade = $Objeto.PSObject.Properties[$Nome]
    if ($null -eq $propriedade -or $null -eq $propriedade.Value) { return '' }
    return [string] $propriedade.Value
}

function Resolve-Versao {
    if (-not [string]::IsNullOrEmpty($env:NGX_VERSION)) {
        return $env:NGX_VERSION
    }

    $canal = $env:NGX_CHANNEL
    if ([string]::IsNullOrEmpty($canal)) { $canal = 'stable' }

    switch ($canal) {
        'stable' { $url = "$UrlApi/releases/latest" }
        'beta'   { $url = "$UrlApi/releases?per_page=1" }
        default  {
            Falha "canal desconhecido: $canal" @(
                '',
                "os valores aceitos sao 'stable' (default) e 'beta'."
            )
        }
    }

    try {
        $resposta = Invoke-RestMethod -Uri $url -UseBasicParsing -ErrorAction Stop
    } catch {
        Falha-DeRelease (Get-CodigoHttp $_) 'a API do GitHub'
    }

    # O canal beta devolve uma lista; @() normaliza o caso de um elemento so,
    # que o PowerShell 5.1 entrega como objeto solto.
    if ($canal -eq 'beta') {
        $lista = @($resposta)
        if ($lista.Count -eq 0) {
            Falha "a API do GitHub respondeu, mas nenhuma release foi encontrada no canal beta" @(
                '',
                'o canal beta lista todas as releases, inclusive pre-lancamentos,',
                'e a lista veio vazia: o projeto ainda nao publicou nenhuma.',
                "confira em $UrlReleases"
            )
        }
        $resposta = $lista[0]
    }

    $tag = Get-PropriedadeTexto $resposta 'tag_name'
    if ([string]::IsNullOrEmpty($tag)) {
        Falha "a API do GitHub respondeu, mas nenhuma release foi encontrada no canal $canal" @(
            '',
            "confira em $UrlReleases. se o projeto so publicou pre-lancamentos",
            "ate agora, use: `$env:NGX_CHANNEL='beta'"
        )
    }

    return $tag
}

# ---------------------------------------------------------------------------
# Fluxo
# ---------------------------------------------------------------------------

$arquitetura = Get-Arquitetura
$diretorio   = Prepara-Diretorio

if (-not (Existe-Comando 'Expand-Archive')) {
    Falha 'o cmdlet Expand-Archive nao esta disponivel' @(
        '',
        'ele vem no PowerShell 5.0 e mais novos. atualize o PowerShell:',
        '  https://aka.ms/powershell'
    )
}

$verificaAssinatura = Avalia-VerificacaoDeAssinatura
$versao             = Resolve-Versao

$diretorioTemporario = Join-Path ([System.IO.Path]::GetTempPath()) ("ngx-install-" + [System.Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $diretorioTemporario -Force | Out-Null

try {
    # O goreleaser deriva o nome do arquivo da versao sem o "v" inicial.
    $versaoSemV   = $versao -replace '^v', ''
    $nomeArquivo  = "ngx_${versaoSemV}_windows_${arquitetura}.zip"
    $baseDownload = "$UrlReleases/download/$versao"

    $caminhoZip       = Join-Path $diretorioTemporario $nomeArquivo
    $caminhoChecksums = Join-Path $diretorioTemporario 'checksums.txt'
    $caminhoAssinatura = Join-Path $diretorioTemporario 'checksums.txt.minisig'

    Escreve-Linha "baixando ngx $versao para windows/$arquitetura..."

    $codigo = Baixa-Arquivo "$baseDownload/$nomeArquivo" $caminhoZip
    if ($codigo -ne 200) {
        if ($codigo -eq 404) {
            # O GitHub responde 404 tanto para tag inexistente quanto para
            # arquivo ausente numa release que existe. Nao da para distinguir
            # pelo codigo, entao a mensagem cobre os dois em vez de afirmar o
            # que nao foi verificado.
            Falha "nao foi possivel baixar $nomeArquivo da release $versao (HTTP 404)" @(
                '',
                'as duas causas possiveis:',
                "  1. a release $versao nao existe. o nome da tag inclui o 'v'",
                '     inicial: v0.1.0, nao 0.1.0.',
                "  2. a release existe mas nao publica o artefato de windows/$arquitetura.",
                '',
                'confira o que existe em:',
                "  $UrlReleases/tag/$versao"
            )
        }
        Falha-DeRelease $codigo 'o download da release' $versao
    }

    $codigo = Baixa-Arquivo "$baseDownload/checksums.txt" $caminhoChecksums
    if ($codigo -ne 200) {
        Falha "a release $versao nao publica checksums.txt (HTTP $codigo)" @(
            '',
            'sem o checksum nao ha como conferir o download, e instalar sem',
            'conferir nao e uma opcao. confira a release em:',
            "  $UrlReleases/tag/$versao"
        )
    }

    if ($verificaAssinatura) {
        $codigo = Baixa-Arquivo "$baseDownload/checksums.txt.minisig" $caminhoAssinatura
        if ($codigo -ne 200) {
            Falha "a release $versao nao publica checksums.txt.minisig (HTTP $codigo)" @(
                '',
                'a chave publica esta neste script, entao a assinatura era',
                'esperada. uma release assinada que perde a assinatura e sinal',
                'de problema no processo de publicacao - nao de algo a contornar.',
                '',
                "confira a release em $UrlReleases/tag/$versao"
            )
        }

        # O codigo de saida do minisign e o que importa, e ele fica em
        # $LASTEXITCODE por ser executavel externo, nao cmdlet.
        #
        # O ErrorActionPreference volta para Continue durante a chamada: com
        # 'Stop', qualquer linha que um executavel externo escreva em stderr
        # vira NativeCommandError e aborta o script com uma mensagem do
        # PowerShell, engolindo a nossa. Aqui quem decide e o codigo de saida.
        $preferenciaAnterior = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        & minisign -V -q -m $caminhoChecksums -x $caminhoAssinatura -P $ChavePublicaMinisign | Out-Null
        $codigoMinisign = $LASTEXITCODE
        $ErrorActionPreference = $preferenciaAnterior

        if ($codigoMinisign -ne 0) {
            Falha 'a assinatura minisign de checksums.txt NAO confere' @(
                '',
                'o arquivo baixado nao foi assinado pela chave do projeto. isso',
                'nao e erro de rede: e um artefato que nao deveria existir.',
                '',
                'nada foi instalado. nao contorne este erro.'
            )
        }

        Escreve-Linha 'assinatura minisign conferida.'
    }

    # O checksums.txt do goreleaser tem uma linha "<sha256>  <arquivo>" por
    # artefato, no formato do sha256sum: dois espacos entre hash e nome.
    $esperado = ''
    foreach ($linha in (Get-Content -LiteralPath $caminhoChecksums)) {
        $partes = $linha -split '\s+', 2
        if ($partes.Count -eq 2 -and $partes[1].Trim() -eq $nomeArquivo) {
            $esperado = $partes[0].Trim()
            break
        }
    }

    if ($esperado -eq '') {
        Falha "checksums.txt nao lista $nomeArquivo" @(
            '',
            "o arquivo de checksums da release $versao nao cobre o artefato",
            'baixado. nada foi instalado.'
        )
    }

    $obtido = (Get-FileHash -LiteralPath $caminhoZip -Algorithm SHA256).Hash

    # -ine: o Get-FileHash devolve maiusculas e o sha256sum minusculas.
    if ($esperado -ine $obtido) {
        Falha "o SHA256 de $nomeArquivo nao confere" @(
            '',
            "  esperado: $esperado",
            "  obtido:   $obtido",
            '',
            'o download veio corrompido ou foi alterado no caminho. nada foi',
            'instalado. tente de novo; se persistir, nao instale este arquivo.'
        )
    }

    Escreve-Linha 'checksum SHA256 conferido.'

    $diretorioExtraido = Join-Path $diretorioTemporario 'extraido'
    Expand-Archive -LiteralPath $caminhoZip -DestinationPath $diretorioExtraido -Force

    $origem = Join-Path $diretorioExtraido 'ngx.exe'
    if (-not (Test-Path -LiteralPath $origem -PathType Leaf)) {
        Falha "o binario ngx.exe nao foi encontrado dentro de $nomeArquivo"
    }

    # Copiar para o destino final e so entao renomear: assim nunca existe um
    # instante em que ngx.exe esta pela metade no diretorio de instalacao.
    $destino        = Join-Path $diretorio 'ngx.exe'
    $destinoParcial = Join-Path $diretorio ".ngx.novo.$PID.exe"
    try {
        Copy-Item -LiteralPath $origem -Destination $destinoParcial -Force
        Move-Item -LiteralPath $destinoParcial -Destination $destino -Force
    } catch {
        if (Test-Path -LiteralPath $destinoParcial) {
            Remove-Item -LiteralPath $destinoParcial -Force -ErrorAction SilentlyContinue
        }
        Falha "nao foi possivel escrever $destino" @(
            '',
            'se o ngx estiver em execucao, o Windows trava o arquivo. feche o',
            'processo e rode de novo.',
            '',
            "detalhe: $($_.Exception.Message)"
        )
    }

    Escreve-Linha "ngx $versao instalado em $destino"

    # PATH do usuario: escopo User, que nao exige elevacao.
    #
    # A leitura e a escrita passam pelo registro em vez de
    # [Environment]::GetEnvironmentVariable/SetEnvironmentVariable porque essa
    # API expande as variaveis embutidas no valor. Um PATH que contenha
    # "%USERPROFILE%\bin" volta da API ja expandido para o caminho literal, e
    # grava-lo de volta destroi a referencia — a pessoa perde a portabilidade
    # do proprio PATH por causa de uma instalacao. Lendo com
    # DoNotExpandEnvironmentNames e regravando com o mesmo tipo (ExpandString),
    # o valor original e preservado.
    $chaveAmbiente = 'HKCU:\Environment'
    $pathDoUsuario = ''
    $tipoDoValor   = [Microsoft.Win32.RegistryValueKind]::ExpandString
    $leituraOk     = $false

    try {
        $chave = Get-Item -LiteralPath $chaveAmbiente
        $valor = $chave.GetValue(
            'Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        if ($null -ne $valor) { $pathDoUsuario = [string] $valor }
        if ($pathDoUsuario -ne '') { $tipoDoValor = $chave.GetValueKind('Path') }
        $leituraOk = $true
    } catch {
        $leituraOk = $false
    }

    $jaEstaNoPath = $false
    foreach ($parte in ($pathDoUsuario -split ';')) {
        if ($parte.Trim() -ne '' -and $parte.Trim().TrimEnd('\') -ieq $diretorio.TrimEnd('\')) {
            $jaEstaNoPath = $true
            break
        }
    }

    if ($jaEstaNoPath) {
        Escreve-Linha "rode 'ngx version' para conferir."
    } elseif (-not $leituraOk) {
        Escreve-Linha ''
        Escreve-Linha 'atencao: nao foi possivel ler o PATH do seu usuario, entao ele nao'
        Escreve-Linha 'foi alterado. acrescente o diretorio manualmente:'
        Escreve-Linha "  $diretorio"
    } else {
        try {
            $novoPath = if ($pathDoUsuario.Trim() -eq '') {
                $diretorio
            } else {
                "$($pathDoUsuario.TrimEnd(';'));$diretorio"
            }
            Set-ItemProperty -LiteralPath $chaveAmbiente -Name 'Path' -Value $novoPath -Type $tipoDoValor
            Escreve-Linha ''
            Escreve-Linha "$diretorio foi acrescentado ao PATH do seu usuario."
            Escreve-Linha 'abra um terminal novo para a mudanca valer - a janela atual'
            Escreve-Linha 'continua com o PATH antigo.'
        } catch {
            Escreve-Linha ''
            Escreve-Linha "atencao: nao foi possivel alterar o PATH do usuario ($($_.Exception.Message))."
            Escreve-Linha 'o ngx foi instalado; so o PATH ficou por fazer. acrescente:'
            Escreve-Linha "  $diretorio"
        }
    }

    Escreve-Linha ''
    Escreve-Linha 'nota sobre Windows: o nginx para Windows e distribuido como build beta'
    Escreve-Linha 'pelo proprio nginx.org e nao e instalado por gerenciador de pacotes.'
    Escreve-Linha 'aponte o ngx para o diretorio desempacotado com -c, por exemplo:'
    Escreve-Linha '  ngx -c C:\nginx-1.31.3\conf\nginx.conf inspect'
}
finally {
    if (Test-Path -LiteralPath $diretorioTemporario) {
        Remove-Item -LiteralPath $diretorioTemporario -Recurse -Force -ErrorAction SilentlyContinue
    }
}
