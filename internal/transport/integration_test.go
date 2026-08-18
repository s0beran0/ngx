//go:build integration

// Testes de integracao do caminho remoto contra a bancada (test/bancada): um
// container descartavel com sshd e nginx, que reproduz a forma medida num
// nginx de producao — tres padroes com curinga, 130 arquivos na configuracao
// efetiva, /etc/nginx legivel so por root e segredo dentro da configuracao.
//
// Ficam atras da tag `integration` porque exigem Docker: `go test ./...` sem
// a tag nao toca em container nenhum. Com a tag e sem a bancada no ar, os
// testes PULAM com a instrucao de como subi-la — quem clonou o projeto e nao
// tem Docker nao pode ver falha falsa.
//
// Rode: make bancada-up && go test -tags integration ./... -race
//
// O pacote e transport_test, e nao transport, porque o teste do privilegio
// explicito (DR5) usa internal/runtime, que importa internal/transport: de
// dentro do pacote isso seria ciclo de importacao.
package transport_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/runtime"
	"github.com/s0beran0/ngx/internal/transport"
)

const (
	hostBancada    = "127.0.0.1"
	usuarioBancada = "ngxtest"
	// A porta e fixa no Makefile para que o teste saiba onde conectar;
	// quem subiu a bancada com BANCADA_PORTA=outra informa pela variavel.
	portaBancadaPadrao = 2222
	envPortaBancada    = "NGX_BANCADA_PORTA"

	// A configuracao efetiva do container tem 130 arquivos, conferidos no
	// proprio build da imagem. A tolerancia acompanha a do smoke.sh: o
	// numero de modulos dinamicos muda se um pacote nginx-mod-* entrar ou
	// sair, e o que o teste prova e a ordem de grandeza, nao o inventario.
	arquivosDaBancada  = 130
	toleranciaArquivos = 5

	// marcadorArmadilha so existe no arquivo homonimo do disco LOCAL
	// (test/bancada/armadilha-local). Ver o arquivo montado no container.
	marcadorArmadilha = "ARMADILHA-LOCAL-NAO-DEVE-APARECER"
	arquivoArmadilha  = "zz-armadilha-local.conf"

	// Os tres segredos da bancada, nas tres formas que ela reproduz.
	tokenDaBancada    = "Bearer ngx-bancada-token-4f3c9a1b2e"
	htpasswdDaBancada = "/etc/nginx/secrets/htpasswd"
	chaveTLSDaBancada = "/etc/nginx/secrets/tls.key"

	// A fixture remota, escrita no HOME do usuario da bancada.
	arquivoTopoRemoto  = "ngx-remoto.conf"
	dirConfDRemoto     = "etc/nginx/conf.d"
	arquivoDoContainer = "10-do-container.conf"
	padraoConfD        = dirConfDRemoto + "/*.conf"
	padraoModulos      = "/usr/share/nginx/modules/*.conf"
)

// ---------------------------------------------------------------------------
// Bancada
// ---------------------------------------------------------------------------

func raizDoRepo(t *testing.T) string {
	t.Helper()
	raiz, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return raiz
}

func portaDaBancada(t *testing.T) int {
	t.Helper()
	valor := os.Getenv(envPortaBancada)
	if valor == "" {
		return portaBancadaPadrao
	}
	porta, err := strconv.Atoi(valor)
	require.NoErrorf(t, err, "%s=%q nao e um numero de porta", envPortaBancada, valor)
	return porta
}

// exigirBancada pula o teste, em vez de falhar, quando a bancada nao esta no
// ar: sem Docker o caminho remoto nao pode ser exercitado, e uma falha ali
// nao diria nada sobre o ngx.
func exigirBancada(t *testing.T) (chave string, porta int) {
	t.Helper()

	chave = filepath.Join(raizDoRepo(t), "test", "bancada", ".chave", "id_ed25519")
	porta = portaDaBancada(t)

	if _, err := os.Stat(chave); err != nil {
		t.Skipf("bancada fora do ar: a chave de teste %s nao existe. "+
			"Rode `make bancada-up` (precisa de Docker).", chave)
	}

	endereco := net.JoinHostPort(hostBancada, strconv.Itoa(porta))
	conn, err := net.DialTimeout("tcp", endereco, 2*time.Second)
	if err != nil {
		t.Skipf("bancada fora do ar: nada escuta em %s (%v). "+
			"Rode `make bancada-up` (precisa de Docker).", endereco, err)
	}
	_ = conn.Close()

	// O ssh-agent de quem roda a suite fica de fora: a bancada so aceita a
	// chave gerada, e um agente com varias chaves esgota o MaxAuthTries do
	// sshd antes de chegar nela. Sem agente, o metodo simplesmente sai da
	// lista — e o caminho que o teste quer exercitar e o da chave.
	t.Setenv(transport.EnvSocketSSHAgent, "")

	return chave, porta
}

func opcoesDaBancada(chave string, porta int, knownHosts string) transport.SSHOptions {
	return transport.SSHOptions{
		Host:           hostBancada,
		Port:           porta,
		User:           usuarioBancada,
		KeyPath:        chave,
		KnownHostsPath: knownHosts,
		Timeout:        20 * time.Second,
	}
}

// prefixoDaLinhaKnownHosts e o que a mensagem de host desconhecido escreve
// antes da linha pronta para o known_hosts.
const prefixoDaLinhaKnownHosts = "acrescente a linha ao arquivo: "

func linhaDoKnownHosts(t *testing.T, err error) string {
	t.Helper()
	var e *output.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, transport.CodigoHostDesconhecido, e.Diag.Code)

	_, linha, achou := strings.Cut(e.Diag.Message, prefixoDaLinhaKnownHosts)
	require.Truef(t, achou,
		"a mensagem de host desconhecido nao trouxe a linha do known_hosts: %s", e.Diag.Message)
	return strings.TrimSpace(linha)
}

// knownHostsAprendido registra a chave de host da bancada num known_hosts
// temporario, aprendendo-a pela propria recusa de primeiro acesso.
//
// Nenhum teste daqui usa --insecure-host-key: todos conectam com verificacao
// de host key de verdade, que e como o ngx roda em producao.
func knownHostsAprendido(t *testing.T, chave string, porta int) string {
	t.Helper()

	caminho := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(caminho, nil, 0o600))

	tr, _, err := transport.SSHComDiagnosticos(opcoesDaBancada(chave, porta, caminho))
	if err == nil {
		_ = tr.Close()
		t.Fatal("a conexao foi aceita com o known_hosts vazio: host desconhecido tem de ser recusado")
	}

	require.NoError(t, os.WriteFile(caminho, []byte(linhaDoKnownHosts(t, err)+"\n"), 0o600))
	return caminho
}

func conectarNaBancada(t *testing.T) transport.Transport {
	t.Helper()

	chave, porta := exigirBancada(t)
	tr, diags, err := transport.SSHComDiagnosticos(
		opcoesDaBancada(chave, porta, knownHostsAprendido(t, chave, porta)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tr.Close() })

	for _, d := range diags {
		require.NotEqualf(t, transport.CodigoAvisoHostKeyInsegura, d.Code,
			"o teste tem de conectar com verificacao de host key: %s", d.Message)
	}
	return tr
}

// ---------------------------------------------------------------------------
// Fixture remota
// ---------------------------------------------------------------------------

// A fixture vive no HOME do usuario da bancada porque /etc/nginx e
// 0700 root:root de proposito — a armadilha da DR5 — e nem `nginx -T` nem uma
// leitura por SFTP alcancam a configuracao real como ngxtest. Os arquivos
// abaixo repetem as formas que importam: um curinga relativo (que resolve
// contra o diretorio do arquivo de topo, e portanto e o que a armadilha do
// disco local disputa), um curinga absoluto sobre arquivos reais do container,
// e as tres formas de segredo da bancada.
const topoRemoto = `# Fixture do teste de integracao do ngx.

# Curinga absoluto: quatro arquivos reais do container, dos pacotes
# nginx-mod-*. Nao existem no disco de quem roda o teste.
include ` + padraoModulos + `;

events {
    worker_connections 1024;
}

http {
    # Curinga relativo, resolvido contra o diretorio do arquivo de topo (o
    # HOME remoto). No disco local, o mesmo padrao cai na armadilha.
    include ` + padraoConfD + `;
}
`

const confDoContainer = `# MARCADOR-DO-CONTAINER
#
# As tres formas de segredo sao as mesmas da configuracao real da bancada
# (test/bancada/gerar-config.sh).
server {
    listen 8080;
    server_name do-container.bancada.local;

    location / {
        auth_basic "area restrita da bancada";
        auth_basic_user_file ` + htpasswdDaBancada + `;

        proxy_set_header Authorization "` + tokenDaBancada + `";
        proxy_pass http://127.0.0.1:9000;
    }
}

server {
    listen 8443 ssl;
    server_name tls.bancada.local;

    ssl_certificate     /etc/nginx/secrets/tls.crt;
    ssl_certificate_key ` + chaveTLSDaBancada + `;

    location / {
        return 200 "tls da bancada\n";
    }
}
`

// montarFixtureRemota escreve a fixture no HOME do container e a remove no
// fim. O `sh -c` esta aqui, no teste, e nao no ngx: o ngx nunca monta linha de
// shell, e o que ele executa no alvo continua sendo argv explicito.
func montarFixtureRemota(t *testing.T, tr transport.Transport) {
	t.Helper()

	script := fmt.Sprintf(`set -e
rm -rf "$HOME/etc" "$HOME/%[1]s"
mkdir -p "$HOME/%[2]s"
cat > "$HOME/%[1]s" <<'FIM'
%[3]s
FIM
cat > "$HOME/%[2]s/%[4]s" <<'FIM'
%[5]s
FIM
`, arquivoTopoRemoto, dirConfDRemoto, topoRemoto, arquivoDoContainer, confDoContainer)

	rodar(t, tr, script)
	t.Cleanup(func() {
		_, _, _, _ = tr.Run(context.Background(),
			[]string{"sh", "-c", fmt.Sprintf(`rm -rf "$HOME/etc" "$HOME/%s"`, arquivoTopoRemoto)})
	})
}

func rodar(t *testing.T, tr transport.Transport, script string) {
	t.Helper()
	ctx, cancelar := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelar()

	stdout, stderr, saida, err := tr.Run(ctx, []string{"sh", "-c", script})
	require.NoError(t, err)
	require.Zerof(t, saida, "montagem da fixture remota falhou: %s %s", stdout, stderr)
}

func caminhosDaArvore(t *config.Tree) []string {
	caminhos := make([]string, 0, len(t.Files))
	for _, f := range t.Files {
		caminhos = append(caminhos, f.Path)
	}
	return caminhos
}

// ---------------------------------------------------------------------------
// 1. O glob resolve os arquivos DO CONTAINER
// ---------------------------------------------------------------------------

// Este e o teste que impede a volta do defeito que a Task R3 corrigiu: com o
// Glob nao injetado, o crossplane resolvia `include conf.d/*.conf` com
// filepath.Glob sobre o disco de quem rodou o ngx, e apresentava os arquivos
// da maquina do operador como configuracao do servidor (DR4).
//
// A armadilha e um arquivo homonimo no disco local. O teste troca o diretorio
// corrente para ele e prova, na mesma execucao, as duas metades: que o padrao
// casa a armadilha localmente (senao o teste passaria por vacuidade), e que a
// arvore lida da bancada tem o arquivo do container e nenhum sinal dela.
func TestGlobResolveOsArquivosDoContainerENaoOsDoDiscoLocal(t *testing.T) {
	armadilha := filepath.Join(raizDoRepo(t), "test", "bancada", "armadilha-local")

	tr := conectarNaBancada(t)
	montarFixtureRemota(t, tr)

	t.Chdir(armadilha)

	locais, err := transport.Local().Glob(padraoConfD)
	require.NoError(t, err)
	require.Containsf(t, locais, filepath.Join(dirConfDRemoto, arquivoArmadilha),
		"a armadilha local nao esta no lugar: %s deveria casar %s a partir de %s",
		padraoConfD, arquivoArmadilha, armadilha)

	arvore, err := config.Parse(config.ParseOptions{
		Path: arquivoTopoRemoto,
		Open: tr.Open,
		Glob: tr.Glob,
	})
	require.NoError(t, err)

	caminhos := caminhosDaArvore(arvore)
	require.Contains(t, caminhos, dirConfDRemoto+"/"+arquivoDoContainer,
		"o curinga relativo nao trouxe o arquivo do container")

	modulos := 0
	for _, f := range arvore.Files {
		require.NotContainsf(t, f.Path, "armadilha",
			"arquivo do disco local entrou na arvore lida da bancada: %s", f.Path)
		require.NotContainsf(t, string(f.Source), marcadorArmadilha,
			"o marcador da armadilha local vazou para a arvore, vindo de %s", f.Path)
		if strings.HasPrefix(f.Path, "/usr/share/nginx/modules/") {
			modulos++
		}
	}
	require.Positive(t, modulos, "o curinga absoluto nao trouxe nenhum modulo do container")
	require.Contains(t, caminhos, arquivoTopoRemoto)
}

// ---------------------------------------------------------------------------
// 2. A configuracao efetiva do container, com os ~130 arquivos
// ---------------------------------------------------------------------------

// Os 130 arquivos so sao alcancaveis por `nginx -T` com privilegio: /etc/nginx
// e 0700 root:root, entao a leitura por SFTP (o caminho do inspect) para no
// primeiro arquivo. Este teste e, portanto, a outra metade do par com o de
// privilegio abaixo: com --sudo o dump existe e e o do container.
func TestDumpRemotoComSudoDevolveAConfiguracaoEfetivaDoContainer(t *testing.T) {
	tr := conectarNaBancada(t)

	ctx, cancelar := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelar()

	dump, err := runtime.New(tr, runtime.ComSudo(true)).DumpConfig(ctx)
	require.NoError(t, err)
	require.True(t, dump.OK)
	require.Contains(t, dump.ConfigFile, "/etc/nginx/nginx.conf")
	require.InDeltaf(t, arquivosDaBancada, len(dump.Files), toleranciaArquivos,
		"a configuracao efetiva do container tem %d arquivos; o dump trouxe %d",
		arquivosDaBancada, len(dump.Files))

	// Os tres curingas da bancada resolveram dentro do container.
	porPrefixo := map[string]int{
		"/etc/nginx/conf.d/":        0,
		"/etc/nginx/default.d/":     0,
		"/usr/share/nginx/modules/": 0,
	}
	for _, f := range dump.Files {
		for prefixo := range porPrefixo {
			if strings.HasPrefix(f.Path, prefixo) {
				porPrefixo[prefixo]++
			}
		}
		require.NotContainsf(t, f.Content, marcadorArmadilha,
			"o marcador da armadilha local apareceu no dump do container, em %s", f.Path)
	}
	for prefixo, n := range porPrefixo {
		require.Positivef(t, n, "nenhum arquivo veio de %s", prefixo)
	}
	require.Greater(t, porPrefixo["/etc/nginx/conf.d/"], 100,
		"conf.d e o diretorio grande da bancada")
}

// ---------------------------------------------------------------------------
// 3. Host desconhecido e recusado ANTES de entrar no known_hosts
// ---------------------------------------------------------------------------

// A DR1 exige duas mensagens diferentes: primeiro acesso e atrito normal,
// chave alterada e possivel ataque. Confundi-las e o defeito perigoso — quem
// le "a chave mudou" num primeiro acesso aprende a ignorar o aviso que um dia
// vai importar de verdade.
func TestHostDesconhecidoEhRecusadoAntesDeEntrarNoKnownHosts(t *testing.T) {
	chave, porta := exigirBancada(t)

	caminho := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(caminho, nil, 0o600))

	tr, _, erroPrimeiroAcesso := transport.SSHComDiagnosticos(opcoesDaBancada(chave, porta, caminho))
	if erroPrimeiroAcesso == nil {
		_ = tr.Close()
		t.Fatal("a conexao foi aceita com o known_hosts vazio")
	}

	var e *output.Error
	require.ErrorAs(t, erroPrimeiroAcesso, &e)
	require.Equal(t, transport.CodigoHostDesconhecido, e.Diag.Code)
	require.NotEqual(t, transport.CodigoHostKeyAlterada, e.Diag.Code)

	msg := e.Diag.Message
	require.Contains(t, msg, "host desconhecido")
	require.Contains(t, msg, "primeiro acesso")
	require.NotContains(t, msg, "MUDOU")
	require.NotContains(t, msg, "ataque")
	require.Equal(t, caminho, e.Diag.File)

	// O ngx nao aprende a chave sozinho: quem registra e o operador.
	conteudo, err := os.ReadFile(caminho)
	require.NoError(t, err)
	require.Empty(t, conteudo, "o known_hosts foi escrito pelo ngx")

	// E com a chave registrada, a mesma conexao passa — a recusa era da
	// verificacao, nao da credencial.
	linha := linhaDoKnownHosts(t, erroPrimeiroAcesso)
	require.NoError(t, os.WriteFile(caminho, []byte(linha+"\n"), 0o600))

	tr, diags, err := transport.SSHComDiagnosticos(opcoesDaBancada(chave, porta, caminho))
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()

	require.Equal(t, fmt.Sprintf("ssh://%s@%s:%d", usuarioBancada, hostBancada, porta), tr.Describe())
	for _, d := range diags {
		require.NotEqual(t, transport.CodigoAvisoHostKeyInsegura, d.Code)
	}
}

// ---------------------------------------------------------------------------
// 4. Privilegio explicito (DR5)
// ---------------------------------------------------------------------------

// A bancada foi construida com esta armadilha: `nginx -T` falha para o
// usuario comum e o sudo sem senha existe, restrito ao binario do nginx. O
// caminho que "simplesmente funciona" seria escalar em silencio; o ngx
// reporta e para.
func TestSemSudoONgxReportaAExigenciaDePrivilegioENaoEscalaSozinho(t *testing.T) {
	tr := conectarNaBancada(t)

	ctx, cancelar := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelar()

	dump, err := runtime.New(tr).DumpConfig(ctx)
	require.Nil(t, dump, "sem privilegio nao ha dump: campo indisponivel e omitido")
	require.Error(t, err)

	var e *output.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, runtime.CodigoPrivilegioNecessario, e.Diag.Code)

	msg := e.Diag.Message
	require.Contains(t, msg, "`nginx -T`", "o comando executado nao levou sudo")
	require.Contains(t, msg, "--sudo")
	require.Contains(t, msg, "sudo -n nginx -T", "a mensagem tem de dizer qual e o comando privilegiado")
	require.NotContains(t, msg, tokenDaBancada)

	// A mesma chamada, com --sudo, funciona: a bancada libera o sudo sem
	// senha para o nginx. Ou seja, a recusa acima foi decisao do ngx, nao
	// falta de caminho.
	dump, err = runtime.New(tr, runtime.ComSudo(true)).DumpConfig(ctx)
	require.NoError(t, err)
	require.True(t, dump.OK)
	require.NotEmpty(t, dump.Files)
}

// ---------------------------------------------------------------------------
// 5. Redacao: os tres segredos da bancada nao vazam
// ---------------------------------------------------------------------------

// Aqui a prova e de que o segredo ESTA na configuracao lida do container — o
// que torna o teste de redacao do CLI (internal/cli) uma prova de verdade, e
// nao de uma configuracao vazia.
func TestOsTresSegredosEstaoNaConfiguracaoLidaDaBancada(t *testing.T) {
	tr := conectarNaBancada(t)
	montarFixtureRemota(t, tr)

	arvore, err := config.Parse(config.ParseOptions{
		Path: arquivoTopoRemoto,
		Open: tr.Open,
		Glob: tr.Glob,
	})
	require.NoError(t, err)

	var texto strings.Builder
	for _, f := range arvore.Files {
		texto.Write(f.Source)
	}
	for _, segredo := range []string{tokenDaBancada, htpasswdDaBancada, chaveTLSDaBancada} {
		require.Containsf(t, texto.String(), segredo,
			"a configuracao lida da bancada nao tem o segredo %q", segredo)
	}
}
