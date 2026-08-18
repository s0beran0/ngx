#!/bin/sh
# Instalador do ngx para Unix (Linux e macOS).
#
#   curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh | sh
#
# Escrito em sh POSIX de proposito: quem roda "curl | sh" pode nao ter bash,
# e o instalador precisa funcionar antes de qualquer dependencia existir.
#
# Ordem deliberada das etapas: tudo que pode falhar sem rede falha ANTES do
# primeiro download — plataforma, diretorio de instalacao, permissao de
# escrita e ferramentas de verificacao. Gastar o download para so entao
# descobrir que falta permissao e desperdicio, e pior: deixa lixo no disco.

set -eu

REPOSITORIO="s0beran0/ngx"
URL_API="https://api.github.com/repos/${REPOSITORIO}"
URL_RELEASES="https://github.com/${REPOSITORIO}/releases"

# ---------------------------------------------------------------------------
# PLACEHOLDER: CHAVE PUBLICA MINISIGN (DD2/DD3)
# ---------------------------------------------------------------------------
# A chave publica do projeto AINDA NAO FOI GERADA (Task D2). O valor abaixo e
# um placeholder proposital e NAO e uma chave: chave minisign real e uma linha
# base64 de 56 caracteres comecando por "RW". O texto foi escrito para ser
# impossivel de confundir com uma chave de verdade — um valor plausivel
# passaria despercebido em revisao e chegaria a producao verificando nada.
#
# Ao gerar a chave (mesma que vai para ngx-minisign.pub e para a variable
# NGX_PUBLIC_KEY do repositorio), substitua a linha abaixo pela linha de chave
# do arquivo .pub — a segunda linha, sem o comentario "untrusted comment:".
#
# Enquanto o placeholder estiver aqui, o script RECUSA instalar: ausencia de
# verificacao e falha, nunca um "segui em frente".
CHAVE_PUBLICA_MINISIGN="RWSZFXRcIf6p0xLvenNPLgltwYLa/qRAjNH3sA238fWZIy49RGIbtgAW"
PLACEHOLDER_CHAVE="PLACEHOLDER-CHAVE-MINISIGN-NAO-GERADA-VER-TASK-D2"

# Configuravel por ambiente. Sem valores default surpreendentes.
# O default usa "-" e nao ":-": NGX_INSTALL_DIR definida e vazia quase sempre
# vem de uma variavel que nao expandiu como a pessoa esperava, e cair no
# /usr/local/bin nesse caso instalaria em lugar diferente do pedido.
NGX_INSTALL_DIR="${NGX_INSTALL_DIR-/usr/local/bin}"
NGX_CHANNEL="${NGX_CHANNEL:-stable}"
NGX_VERSION="${NGX_VERSION:-}"
NGX_ALLOW_UNVERIFIED="${NGX_ALLOW_UNVERIFIED:-0}"

DIRETORIO_TEMPORARIO=""
ARQUIVO_PARCIAL=""
FERRAMENTA_HTTP=""
FERRAMENTA_SHA256=""
ASSINATURA_VERIFICADA=0

# ---------------------------------------------------------------------------
# Utilidades
# ---------------------------------------------------------------------------

erro() {
    printf 'erro: %s\n' "$1" >&2
}

linha() {
    printf '%s\n' "$1" >&2
}

informa() {
    printf '%s\n' "$1" >&2
}

limpar() {
    if [ -n "$ARQUIVO_PARCIAL" ] && [ -e "$ARQUIVO_PARCIAL" ]; then
        rm -f "$ARQUIVO_PARCIAL"
    fi
    if [ -n "$DIRETORIO_TEMPORARIO" ] && [ -d "$DIRETORIO_TEMPORARIO" ]; then
        rm -rf "$DIRETORIO_TEMPORARIO"
    fi
}
trap limpar EXIT
trap 'limpar; exit 130' INT
trap 'limpar; exit 143' TERM
trap 'limpar; exit 129' HUP

ajuda() {
    cat <<'FIM'
install.sh — instalador do ngx para Linux e macOS

USO
  curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh | sh
  sh install.sh [--help]

VARIAVEIS DE AMBIENTE
  NGX_INSTALL_DIR      Diretorio de instalacao. Default: /usr/local/bin
                       Um diretorio do sistema exige privilegio; o script nunca
                       chama sudo sozinho, so diz a linha exata a rodar.
  NGX_CHANNEL          stable (default) ou beta. beta inclui pre-lancamentos
                       (-rc, -beta, -alpha).
  NGX_VERSION          Versao fixa, ex: v0.2.0. Quando definida, a API do
                       GitHub nao e consultada.
  NGX_ALLOW_UNVERIFIED Se 1, permite instalar quando a assinatura minisign nao
                       PODE ser verificada (minisign ausente ou chave publica
                       ainda nao gerada). O aviso e impresso em destaque. NAO
                       ignora assinatura invalida nem checksum divergente:
                       esses abortam sempre, sem excecao.

EXEMPLOS
  # instalacao no sistema, com privilegio
  curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh | sudo sh

  # instalacao sem privilegio, no diretorio do usuario
  curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh \
    | NGX_INSTALL_DIR=$HOME/.local/bin sh

  # versao fixa
  curl -fsSL https://raw.githubusercontent.com/s0beran0/ngx/main/install.sh \
    | NGX_VERSION=v0.2.0 sh

VERIFICACAO
  O checksum SHA256 e conferido sempre e nao tem como ser desligado.
  A assinatura minisign do checksums.txt e conferida quando o minisign esta
  instalado e a chave publica do projeto esta embutida neste script.
FIM
}

existe() {
    command -v "$1" >/dev/null 2>&1
}

# ---------------------------------------------------------------------------
# Etapa 1 — argumentos
# ---------------------------------------------------------------------------

for argumento in "$@"; do
    case "$argumento" in
        -h|--help)
            ajuda
            exit 0
            ;;
        *)
            erro "argumento desconhecido: $argumento"
            linha ""
            linha "rode 'sh install.sh --help' para ver as opcoes."
            exit 2
            ;;
    esac
done

# ---------------------------------------------------------------------------
# Etapa 2 — plataforma
# ---------------------------------------------------------------------------

detectar_plataforma() {
    sistema_cru="$(uname -s)"
    arquitetura_crua="$(uname -m)"

    case "$sistema_cru" in
        Linux)  SISTEMA="linux" ;;
        Darwin) SISTEMA="darwin" ;;
        MINGW*|MSYS*|CYGWIN*|Windows_NT)
            erro "este script e para Linux e macOS; o sistema detectado foi $sistema_cru"
            linha ""
            linha "no Windows use o instalador em PowerShell:"
            linha "  irm https://raw.githubusercontent.com/${REPOSITORIO}/main/install.ps1 | iex"
            exit 1
            ;;
        *)
            erro "sistema operacional nao suportado: $sistema_cru"
            linha ""
            linha "o ngx publica binarios para linux e darwin (macOS)."
            linha "para outras plataformas, compile do fonte:"
            linha "  git clone https://github.com/${REPOSITORIO}.git && cd ngx && make build"
            exit 1
            ;;
    esac

    case "$arquitetura_crua" in
        x86_64|amd64)   ARQUITETURA="amd64" ;;
        aarch64|arm64)  ARQUITETURA="arm64" ;;
        *)
            erro "arquitetura nao suportada: ${sistema_cru}/${arquitetura_crua}"
            linha ""
            linha "o ngx publica binarios para amd64 (x86_64) e arm64 (aarch64)."
            linha "para ${arquitetura_crua}, compile do fonte:"
            linha "  git clone https://github.com/${REPOSITORIO}.git && cd ngx && make build"
            exit 1
            ;;
    esac
}

# ---------------------------------------------------------------------------
# Etapa 3 — diretorio de instalacao e permissao
# ---------------------------------------------------------------------------
#
# O script NAO chama sudo. Escalar privilegio por conta propria dentro de algo
# que se executa via "curl | sh" e exatamente o que ninguem deveria aceitar
# rodar: quem decide elevar e a pessoa, com o comando na frente dos olhos.

mensagem_de_privilegio() {
    motivo="$1"
    erro "$motivo"
    linha ""
    linha "rode a instalacao com privilegio:"
    linha "  curl -fsSL https://raw.githubusercontent.com/${REPOSITORIO}/main/install.sh | sudo sh"
    linha ""
    linha "ou instale num diretorio seu, sem privilegio:"
    linha "  curl -fsSL https://raw.githubusercontent.com/${REPOSITORIO}/main/install.sh | NGX_INSTALL_DIR=\$HOME/.local/bin sh"
    linha ""
    linha "se escolher a segunda, garanta que \$HOME/.local/bin esta no PATH."
    exit 1
}

preparar_diretorio() {
    if [ -z "$NGX_INSTALL_DIR" ]; then
        erro "NGX_INSTALL_DIR esta definida e vazia"
        linha ""
        linha "deixe a variavel indefinida para usar /usr/local/bin, ou aponte"
        linha "para um diretorio: NGX_INSTALL_DIR=\$HOME/.local/bin"
        exit 2
    fi

    if [ -e "$NGX_INSTALL_DIR" ] && [ ! -d "$NGX_INSTALL_DIR" ]; then
        erro "$NGX_INSTALL_DIR existe e nao e um diretorio"
        exit 1
    fi

    if [ ! -d "$NGX_INSTALL_DIR" ]; then
        if ! mkdir -p "$NGX_INSTALL_DIR" 2>/dev/null; then
            mensagem_de_privilegio "nao foi possivel criar o diretorio $NGX_INSTALL_DIR"
        fi
    fi

    # A escrita real e o unico teste que nao mente: [ -w ] erra em sistema de
    # arquivos montado somente leitura, em ACL e em container com usuario
    # mapeado.
    arquivo_de_teste="${NGX_INSTALL_DIR}/.ngx-teste-de-escrita.$$"
    if ! (umask 077 && : > "$arquivo_de_teste") 2>/dev/null; then
        mensagem_de_privilegio "sem permissao de escrita em $NGX_INSTALL_DIR"
    fi
    rm -f "$arquivo_de_teste"
}

# ---------------------------------------------------------------------------
# Etapa 4 — ferramentas e verificacao (antes de baixar)
# ---------------------------------------------------------------------------

escolher_ferramenta_http() {
    if existe curl; then
        FERRAMENTA_HTTP="curl"
    elif existe wget; then
        FERRAMENTA_HTTP="wget"
    else
        erro "nem curl nem wget foram encontrados"
        linha ""
        linha "instale um dos dois e rode de novo. em Debian/Ubuntu:"
        linha "  apt-get install -y curl"
        exit 1
    fi
}

escolher_ferramenta_sha256() {
    if existe sha256sum; then
        FERRAMENTA_SHA256="sha256sum"
    elif existe shasum; then
        FERRAMENTA_SHA256="shasum"
    else
        erro "nenhuma ferramenta de SHA256 encontrada (sha256sum ou shasum)"
        linha ""
        linha "o checksum e obrigatorio e nao tem como ser desligado: instalar"
        linha "um binario sem conferir o hash aceitaria qualquer download"
        linha "corrompido ou trocado no caminho."
        linha ""
        linha "em Debian/Ubuntu: apt-get install -y coreutils"
        linha "em Alpine:        apk add coreutils"
        exit 1
    fi
}

# Decide, ANTES de baixar, se a assinatura podera ser verificada. Sao tres
# desfechos, e nenhum deles e silencioso:
#   - da para verificar          -> segue, e a assinatura sera conferida
#   - nao da, sem autorizacao    -> aborta aqui, dizendo por que e como resolver
#   - nao da, com autorizacao    -> segue com aviso em destaque
avaliar_verificacao_de_assinatura() {
    motivo=""

    if [ "$CHAVE_PUBLICA_MINISIGN" = "$PLACEHOLDER_CHAVE" ]; then
        motivo="a chave publica minisign do projeto ainda nao foi gerada e este script carrega um placeholder"
    elif ! existe minisign; then
        motivo="o minisign nao esta instalado nesta maquina"
    fi

    if [ -z "$motivo" ]; then
        ASSINATURA_VERIFICADA=1
        return 0
    fi

    if [ "$NGX_ALLOW_UNVERIFIED" = "1" ]; then
        ASSINATURA_VERIFICADA=0
        linha ""
        linha "############################################################"
        linha "# AVISO: INSTALANDO SEM VERIFICAR A ASSINATURA"
        linha "#"
        linha "# $motivo."
        linha "#"
        linha "# NGX_ALLOW_UNVERIFIED=1 esta definida, entao a instalacao"
        linha "# segue. O checksum SHA256 ainda sera conferido, mas ele so"
        linha "# protege contra download corrompido: nao protege contra um"
        linha "# release publicado por quem tenha comprometido a conta do"
        linha "# GitHub, porque nesse caso o checksum viria adulterado junto."
        linha "############################################################"
        linha ""
        return 0
    fi

    erro "a assinatura do release nao pode ser verificada"
    linha ""
    linha "motivo: $motivo."
    linha ""
    linha "o ngx roda como root em servidores que servem trafego. instalar um"
    linha "binario sem verificar de onde ele veio nao e um detalhe de higiene."
    linha "por isso o script para aqui em vez de seguir em frente."
    linha ""
    linha "como resolver:"
    if [ "$CHAVE_PUBLICA_MINISIGN" = "$PLACEHOLDER_CHAVE" ]; then
        linha "  a chave publica ainda nao existe — nao ha o que instalar do seu"
        linha "  lado. acompanhe ${URL_RELEASES} e use uma versao"
        linha "  deste script publicada depois da primeira release assinada."
    else
        linha "  instale o minisign e rode de novo:"
        linha "    Debian/Ubuntu: apt-get install -y minisign"
        linha "    Alpine:        apk add minisign"
        linha "    macOS:         brew install minisign"
    fi
    linha ""
    linha "se voce aceita o risco de forma consciente, e so nesse caso:"
    linha "  NGX_ALLOW_UNVERIFIED=1 sh install.sh"
    exit 1
}

# ---------------------------------------------------------------------------
# Etapa 5 — resolucao da versao
# ---------------------------------------------------------------------------

# Baixa uma URL para um arquivo e imprime o codigo HTTP no stdout.
baixar_para() {
    url="$1"
    destino="$2"

    if [ "$FERRAMENTA_HTTP" = "curl" ]; then
        curl --proto '=https' --tlsv1.2 -sSL \
            --connect-timeout 15 --retry 2 --retry-delay 1 \
            -o "$destino" -w '%{http_code}' "$url" 2>/dev/null || printf '000'
    else
        if wget -q --timeout=15 --tries=3 -O "$destino" "$url" 2>/dev/null; then
            printf '200'
        else
            printf '000'
        fi
    fi
}

falha_de_release() {
    codigo="$1"
    onde="$2"

    case "$codigo" in
        404)
            erro "nenhuma release encontrada para ${REPOSITORIO} (${onde} respondeu 404)"
            linha ""
            linha "as duas causas possiveis:"
            linha "  1. o projeto ainda nao publicou nenhuma release. confira em"
            linha "     ${URL_RELEASES}"
            if [ -n "$NGX_VERSION" ]; then
                linha "  2. a versao pedida, ${NGX_VERSION}, nao existe. o nome da tag"
                linha "     inclui o 'v' inicial: NGX_VERSION=v0.1.0, nao 0.1.0."
            else
                linha "  2. so existem pre-lancamentos. tente o canal beta:"
                linha "     NGX_CHANNEL=beta sh install.sh"
            fi
            ;;
        403|429)
            erro "a API do GitHub recusou a consulta (HTTP ${codigo}) — provavel limite de requisicoes por IP"
            linha ""
            linha "o limite anonimo e por hora e por endereco. duas saidas:"
            linha "  - espere e tente de novo, ou"
            linha "  - fixe a versao, que dispensa a consulta a API:"
            linha "      NGX_VERSION=v0.1.0 sh install.sh"
            ;;
        000)
            erro "nao foi possivel falar com ${onde}"
            linha ""
            linha "verifique a conexao de rede, o DNS e se ha proxy exigindo"
            linha "configuracao (https_proxy). nenhum arquivo foi escrito."
            ;;
        *)
            erro "resposta inesperada de ${onde}: HTTP ${codigo}"
            linha ""
            linha "confira o estado do servico em https://www.githubstatus.com"
            ;;
    esac
    exit 1
}

# Extrai o primeiro "tag_name" de um JSON de release. Sem jq: o instalador nao
# pode depender de ferramenta que a maquina talvez nao tenha.
primeira_tag() {
    tr ',' '\n' < "$1" \
        | grep -m 1 '"tag_name"' \
        | sed -e 's/.*"tag_name"[[:space:]]*:[[:space:]]*"//' -e 's/".*//'
}

resolver_versao() {
    if [ -n "$NGX_VERSION" ]; then
        VERSAO="$NGX_VERSION"
        return 0
    fi

    case "$NGX_CHANNEL" in
        stable) url_consulta="${URL_API}/releases/latest" ;;
        beta)   url_consulta="${URL_API}/releases?per_page=1" ;;
        *)
            erro "canal desconhecido: $NGX_CHANNEL"
            linha ""
            linha "os valores aceitos sao 'stable' (default) e 'beta'."
            exit 2
            ;;
    esac

    resposta="${DIRETORIO_TEMPORARIO}/release.json"
    codigo="$(baixar_para "$url_consulta" "$resposta")"

    if [ "$codigo" != "200" ]; then
        falha_de_release "$codigo" "a API do GitHub"
    fi

    VERSAO="$(primeira_tag "$resposta")"

    if [ -z "$VERSAO" ]; then
        erro "a API do GitHub respondeu, mas nenhuma release foi encontrada no canal ${NGX_CHANNEL}"
        linha ""
        if [ "$NGX_CHANNEL" = "beta" ]; then
            linha "o canal beta lista todas as releases, inclusive pre-lancamentos,"
            linha "e a lista veio vazia: o projeto ainda nao publicou nenhuma."
            linha "confira em ${URL_RELEASES}"
        else
            linha "confira em ${URL_RELEASES}. se o projeto so publicou"
            linha "pre-lancamentos ate agora, use: NGX_CHANNEL=beta sh install.sh"
        fi
        exit 1
    fi
}

# ---------------------------------------------------------------------------
# Etapa 6 — download e verificacao
# ---------------------------------------------------------------------------

sha256_de() {
    if [ "$FERRAMENTA_SHA256" = "sha256sum" ]; then
        sha256sum "$1" | cut -d ' ' -f 1
    else
        shasum -a 256 "$1" | cut -d ' ' -f 1
    fi
}

baixar_artefatos() {
    # O goreleaser deriva o nome do arquivo da versao sem o "v" inicial
    # (name_template usa .Version, que ja vem sem prefixo).
    versao_sem_v="${VERSAO#v}"
    NOME_ARQUIVO="ngx_${versao_sem_v}_${SISTEMA}_${ARQUITETURA}.tar.gz"
    base_download="${URL_RELEASES}/download/${VERSAO}"

    CAMINHO_TARBALL="${DIRETORIO_TEMPORARIO}/${NOME_ARQUIVO}"
    CAMINHO_CHECKSUMS="${DIRETORIO_TEMPORARIO}/checksums.txt"

    informa "baixando ngx ${VERSAO} para ${SISTEMA}/${ARQUITETURA}..."

    codigo="$(baixar_para "${base_download}/${NOME_ARQUIVO}" "$CAMINHO_TARBALL")"
    if [ "$codigo" != "200" ]; then
        if [ "$codigo" = "404" ]; then
            # O GitHub responde 404 tanto para tag inexistente quanto para
            # arquivo ausente numa release que existe. Nao da para distinguir
            # os dois pelo codigo, entao a mensagem cobre os dois em vez de
            # afirmar o que nao foi verificado.
            erro "nao foi possivel baixar ${NOME_ARQUIVO} da release ${VERSAO} (HTTP 404)"
            linha ""
            linha "as duas causas possiveis:"
            linha "  1. a release ${VERSAO} nao existe. o nome da tag inclui o 'v'"
            linha "     inicial: NGX_VERSION=v0.1.0, nao 0.1.0."
            linha "  2. a release existe mas nao publica o artefato de"
            linha "     ${SISTEMA}/${ARQUITETURA}."
            linha ""
            linha "confira o que existe em:"
            linha "  ${URL_RELEASES}/tag/${VERSAO}"
            exit 1
        fi
        falha_de_release "$codigo" "o download da release"
    fi

    codigo="$(baixar_para "${base_download}/checksums.txt" "$CAMINHO_CHECKSUMS")"
    if [ "$codigo" != "200" ]; then
        erro "a release ${VERSAO} nao publica checksums.txt (HTTP ${codigo})"
        linha ""
        linha "sem o checksum nao ha como conferir o download, e instalar sem"
        linha "conferir nao e uma opcao. confira a release em:"
        linha "  ${URL_RELEASES}/tag/${VERSAO}"
        exit 1
    fi
}

verificar_assinatura() {
    if [ "$ASSINATURA_VERIFICADA" != "1" ]; then
        return 0
    fi

    caminho_assinatura="${CAMINHO_CHECKSUMS}.minisig"
    codigo="$(baixar_para "${URL_RELEASES}/download/${VERSAO}/checksums.txt.minisig" "$caminho_assinatura")"

    if [ "$codigo" != "200" ]; then
        erro "a release ${VERSAO} nao publica checksums.txt.minisig (HTTP ${codigo})"
        linha ""
        linha "a chave publica esta neste script, entao a assinatura era"
        linha "esperada. uma release assinada que perde a assinatura e sinal de"
        linha "problema no processo de publicacao — nao de algo a contornar."
        linha ""
        linha "confira a release em ${URL_RELEASES}/tag/${VERSAO}"
        exit 1
    fi

    if ! minisign -V -m "$CAMINHO_CHECKSUMS" -x "$caminho_assinatura" \
        -P "$CHAVE_PUBLICA_MINISIGN" >/dev/null 2>&1; then
        erro "a assinatura minisign de checksums.txt NAO confere"
        linha ""
        linha "o arquivo baixado nao foi assinado pela chave do projeto. isso"
        linha "nao e erro de rede: e um artefato que nao deveria existir."
        linha ""
        linha "nada foi instalado. nao contorne este erro."
        exit 1
    fi

    informa "assinatura minisign conferida."
}

verificar_checksum() {
    esperado="$(grep -F "  ${NOME_ARQUIVO}" "$CAMINHO_CHECKSUMS" 2>/dev/null \
        | head -n 1 | cut -d ' ' -f 1)"

    if [ -z "$esperado" ]; then
        erro "checksums.txt nao lista ${NOME_ARQUIVO}"
        linha ""
        linha "o arquivo de checksums da release ${VERSAO} nao cobre o artefato"
        linha "baixado. nada foi instalado."
        exit 1
    fi

    obtido="$(sha256_de "$CAMINHO_TARBALL")"

    if [ "$esperado" != "$obtido" ]; then
        erro "o SHA256 de ${NOME_ARQUIVO} nao confere"
        linha ""
        linha "  esperado: ${esperado}"
        linha "  obtido:   ${obtido}"
        linha ""
        linha "o download veio corrompido ou foi alterado no caminho. nada foi"
        linha "instalado. tente de novo; se persistir, nao instale este arquivo."
        exit 1
    fi

    informa "checksum SHA256 conferido."
}

# ---------------------------------------------------------------------------
# Etapa 7 — instalacao
# ---------------------------------------------------------------------------

instalar() {
    diretorio_extraido="${DIRETORIO_TEMPORARIO}/extraido"
    mkdir -p "$diretorio_extraido"

    if ! tar -xzf "$CAMINHO_TARBALL" -C "$diretorio_extraido" 2>/dev/null; then
        erro "nao foi possivel extrair ${NOME_ARQUIVO}"
        linha ""
        linha "o checksum conferia, entao o arquivo chegou intacto: o problema"
        linha "esta na extracao. verifique se o tar desta maquina suporta gzip."
        exit 1
    fi

    if [ ! -f "${diretorio_extraido}/ngx" ]; then
        erro "o binario ngx nao foi encontrado dentro de ${NOME_ARQUIVO}"
        exit 1
    fi

    # Copiar para o destino final e so entao renomear: o mv dentro do mesmo
    # sistema de arquivos e atomico, entao nunca existe um instante em que
    # $NGX_INSTALL_DIR/ngx esta pela metade. Um cp direto por cima do binario
    # deixaria essa janela aberta.
    ARQUIVO_PARCIAL="${NGX_INSTALL_DIR}/.ngx.novo.$$"
    cp "${diretorio_extraido}/ngx" "$ARQUIVO_PARCIAL"
    chmod 0755 "$ARQUIVO_PARCIAL"
    mv -f "$ARQUIVO_PARCIAL" "${NGX_INSTALL_DIR}/ngx"
    ARQUIVO_PARCIAL=""

    informa "ngx ${VERSAO} instalado em ${NGX_INSTALL_DIR}/ngx"

    case ":${PATH}:" in
        *":${NGX_INSTALL_DIR}:"*)
            informa "rode 'ngx version' para conferir."
            ;;
        *)
            informa ""
            informa "atencao: ${NGX_INSTALL_DIR} nao esta no PATH."
            informa "acrescente a linha abaixo ao seu ~/.profile ou ~/.zshrc:"
            informa "  export PATH=\"${NGX_INSTALL_DIR}:\$PATH\""
            ;;
    esac
}

# ---------------------------------------------------------------------------
# Fluxo
# ---------------------------------------------------------------------------

detectar_plataforma
preparar_diretorio
escolher_ferramenta_http
escolher_ferramenta_sha256
avaliar_verificacao_de_assinatura

DIRETORIO_TEMPORARIO="$(mktemp -d "${TMPDIR:-/tmp}/ngx-install.XXXXXX")"

resolver_versao
baixar_artefatos
verificar_assinatura
verificar_checksum
instalar
