//go:build integration

// Integracao do CLI pelo caminho remoto, contra a bancada (test/bancada).
//
// O que se prova aqui e o que so aparece no fim da linha: o `inspect --host`
// devolvendo a arvore lida do container, o alvo no meta do envelope, e a
// redacao dos tres segredos antes de a saida chegar a quem consome. A camada
// de transporte tem os seus proprios testes em internal/transport.
//
// Atras da tag `integration`, e PULA quando a bancada nao esta no ar.
// Rode: make bancada-up && go test -tags integration ./... -race
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/transport"
)

const (
	hostBancada        = "127.0.0.1"
	usuarioBancada     = "ngxtest"
	portaBancadaPadrao = 2222
	envPortaBancada    = "NGX_BANCADA_PORTA"

	marcadorArmadilha = "ARMADILHA-LOCAL-NAO-DEVE-APARECER"
	arquivoArmadilha  = "zz-armadilha-local.conf"

	// Os tres segredos da bancada, nas tres formas que ela reproduz. Sao os
	// mesmos de test/bancada/gerar-config.sh.
	tokenDaBancada    = "Bearer ngx-bancada-token-4f3c9a1b2e"
	htpasswdDaBancada = "/etc/nginx/secrets/htpasswd"
	chaveTLSDaBancada = "/etc/nginx/secrets/tls.key"

	arquivoTopoRemoto  = "ngx-remoto.conf"
	dirConfDRemoto     = "etc/nginx/conf.d"
	arquivoDoContainer = "10-do-container.conf"
)

// A fixture vive no HOME do usuario da bancada porque /etc/nginx e
// 0700 root:root de proposito (a armadilha da DR5): como ngxtest, nem
// `nginx -T` nem a leitura por SFTP alcancam a configuracao real. O curinga
// relativo e o que disputa com o arquivo homonimo do disco local.
const topoRemotoCLI = `include /usr/share/nginx/modules/*.conf;

events {
    worker_connections 1024;
}

http {
    include ` + dirConfDRemoto + `/*.conf;
}
`

const confDoContainerCLI = `server {
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

func raizDoRepo(t *testing.T) string {
	t.Helper()
	raiz, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return raiz
}

// exigirBancada pula o teste, em vez de falhar, quando a bancada nao esta no
// ar: quem nao tem Docker nao pode ver falha falsa.
func exigirBancada(t *testing.T) (chave string, porta int) {
	t.Helper()

	chave = filepath.Join(raizDoRepo(t), "test", "bancada", ".chave", "id_ed25519")
	porta = portaBancadaPadrao
	if valor := os.Getenv(envPortaBancada); valor != "" {
		var err error
		porta, err = strconv.Atoi(valor)
		require.NoErrorf(t, err, "%s=%q nao e um numero de porta", envPortaBancada, valor)
	}

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

	// Sem o ssh-agent de quem roda a suite: a bancada so aceita a chave
	// gerada, e um agente com varias chaves esgota o MaxAuthTries do sshd.
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

// knownHostsAprendido registra a chave de host da bancada aprendendo-a pela
// propria recusa de primeiro acesso. Nenhum teste daqui usa
// --insecure-host-key: o CLI conecta com verificacao de verdade.
func knownHostsAprendido(t *testing.T, chave string, porta int) string {
	t.Helper()

	caminho := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(caminho, nil, 0o600))

	tr, _, err := transport.SSHComDiagnosticos(opcoesDaBancada(chave, porta, caminho))
	if err == nil {
		_ = tr.Close()
		t.Fatal("a conexao foi aceita com o known_hosts vazio")
	}

	var e *output.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, transport.CodigoHostDesconhecido, e.Diag.Code)

	const prefixo = "acrescente a linha ao arquivo: "
	_, linha, achou := strings.Cut(e.Diag.Message, prefixo)
	require.Truef(t, achou, "a mensagem nao trouxe a linha do known_hosts: %s", e.Diag.Message)

	require.NoError(t, os.WriteFile(caminho, []byte(strings.TrimSpace(linha)+"\n"), 0o600))
	return caminho
}

// montarFixtureRemota escreve a fixture no HOME do container e a remove no
// fim. O `sh -c` esta aqui, no teste, e nao no ngx: o ngx nunca monta linha de
// shell, e o que ele executa no alvo continua sendo argv explicito.
func montarFixtureRemota(t *testing.T, chave string, porta int, knownHosts string) {
	t.Helper()

	tr, _, err := transport.SSHComDiagnosticos(opcoesDaBancada(chave, porta, knownHosts))
	require.NoError(t, err)
	defer func() { _ = tr.Close() }()

	script := fmt.Sprintf(`set -e
rm -rf "$HOME/etc" "$HOME/%[1]s"
mkdir -p "$HOME/%[2]s"
cat > "$HOME/%[1]s" <<'FIM'
%[3]s
FIM
cat > "$HOME/%[2]s/%[4]s" <<'FIM'
%[5]s
FIM
`, arquivoTopoRemoto, dirConfDRemoto, topoRemotoCLI, arquivoDoContainer, confDoContainerCLI)

	ctx, cancelar := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelar()

	stdout, stderr, saida, err := tr.Run(ctx, []string{"sh", "-c", script})
	require.NoError(t, err)
	require.Zerof(t, saida, "montagem da fixture remota falhou: %s %s", stdout, stderr)

	t.Cleanup(func() {
		limpeza, _, err := transport.SSHComDiagnosticos(opcoesDaBancada(chave, porta, knownHosts))
		if err != nil {
			return
		}
		defer func() { _ = limpeza.Close() }()
		_, _, _, _ = limpeza.Run(context.Background(),
			[]string{"sh", "-c", fmt.Sprintf(`rm -rf "$HOME/etc" "$HOME/%s"`, arquivoTopoRemoto)})
	})
}

// arvoreDoEnvelope decodifica so o que este teste observa do data do inspect.
type arvoreDoEnvelope struct {
	Config []struct {
		File string `json:"file"`
	} `json:"config"`
	Summary Summary `json:"summary"`
}

func dataDoInspect(t *testing.T, bruto []byte) arvoreDoEnvelope {
	t.Helper()
	var env struct {
		Data arvoreDoEnvelope `json:"data"`
	}
	require.NoError(t, json.Unmarshal(bruto, &env))
	return env.Data
}

// argumentosDeConexao monta as flags de acesso remoto da bancada.
func argumentosDeConexao(chave string, porta int, knownHosts string) []string {
	return []string{
		"--host", hostBancada,
		"--port", strconv.Itoa(porta),
		"--user", usuarioBancada,
		"--key", chave,
		"--known-hosts", knownHosts,
		"--json",
	}
}

// O inspect remoto ponta a ponta: a arvore vem do container, o alvo aparece no
// meta, e nenhum dos tres segredos atravessa a saida.
//
// O diretorio corrente e o da armadilha do glob (test/bancada/armadilha-local),
// que tem um arquivo homonimo ao do container. Se o Glob nao estivesse
// injetado no parser, o marcador dele apareceria aqui — e o ngx estaria
// apresentando arquivos da maquina do operador como configuracao do servidor.
func TestInspectRemotoLeOContainerEnaoVazaSegredo(t *testing.T) {
	chave, porta := exigirBancada(t)
	knownHosts := knownHostsAprendido(t, chave, porta)
	montarFixtureRemota(t, chave, porta, knownHosts)

	armadilha := filepath.Join(raizDoRepo(t), "test", "bancada", "armadilha-local")
	t.Chdir(armadilha)

	// Controle: no disco local o mesmo padrao casa a armadilha. Sem esta
	// prova, o teste passaria por vacuidade num diretorio vazio.
	locais, err := transport.Local().Glob(dirConfDRemoto + "/*.conf")
	require.NoError(t, err)
	require.Contains(t, locais, filepath.Join(dirConfDRemoto, arquivoArmadilha),
		"a armadilha local nao esta no lugar")

	ctx, out := contextoDeTeste(t, nil)
	ctx.SSHConfigPath = sshConfigDeTeste(t, "")

	args := append(argumentosDeConexao(chave, porta, knownHosts),
		"-c", arquivoTopoRemoto, "inspect")

	var errBuf bytes.Buffer
	code := executar(NewRoot(ctx), ctx, args, &errBuf)
	require.Equalf(t, output.ExitOK, code, "stderr: %s\nstdout: %s", errBuf.String(), out.String())

	env := envelopeDe(t, out)
	require.True(t, env.OK)
	// O unico diagnostico esperado e o informativo de ssh-agent ausente,
	// que o proprio teste provoca ao limpar SSH_AUTH_SOCK.
	for _, d := range env.Diagnostics {
		require.NotEqualf(t, output.SeverityError, d.Severity, "diagnostico de erro: %s", d.Message)
	}
	require.Equal(t, fmt.Sprintf("ssh://%s@%s:%d", usuarioBancada, hostBancada, porta), env.Meta.Target)
	require.NotEmpty(t, env.Meta.ConfigHash)

	// A arvore e a do container: o arquivo do curinga relativo, mais os
	// modulos reais que o curinga absoluto trouxe.
	data := dataDoInspect(t, out.Bytes())
	var caminhos []string
	modulos := 0
	for _, f := range data.Config {
		caminhos = append(caminhos, f.File)
		if strings.HasPrefix(f.File, "/usr/share/nginx/modules/") {
			modulos++
		}
	}
	require.Contains(t, caminhos, dirConfDRemoto+"/"+arquivoDoContainer)
	require.Positive(t, modulos, "o curinga absoluto nao trouxe nenhum modulo do container")
	require.Equal(t, 2, data.Summary.Servers)

	bruto := out.String()
	require.NotContains(t, bruto, marcadorArmadilha,
		"a configuracao do disco local vazou para a arvore lida da bancada")
	require.NotContains(t, bruto, arquivoArmadilha)

	// Redacao: nenhuma das tres formas de segredo sai na saida, e as
	// diretivas continuam visiveis — sumir com o no faria o agente concluir
	// que a diretiva nao existe.
	for _, segredo := range []string{tokenDaBancada, htpasswdDaBancada, chaveTLSDaBancada} {
		require.NotContainsf(t, bruto, segredo, "o segredo %q vazou na saida", segredo)
	}
	require.GreaterOrEqualf(t, strings.Count(bruto, output.RedactedValue), 3,
		"as tres diretivas sensiveis tem de sair redigidas, nao omitidas: %s", bruto)
	for _, diretiva := range []string{"proxy_set_header", "auth_basic_user_file", "ssl_certificate_key"} {
		require.Contains(t, bruto, diretiva)
	}
}

// A outra metade da DR5, no caminho de leitura: como ngxtest, /etc/nginx e
// ilegivel por SFTP. O ngx reporta a recusa em vez de devolver uma arvore
// vazia — e nao repete a leitura com privilegio por conta propria.
func TestInspectRemotoDaConfigRealReportaFaltaDePermissao(t *testing.T) {
	chave, porta := exigirBancada(t)
	knownHosts := knownHostsAprendido(t, chave, porta)

	ctx, out := contextoDeTeste(t, nil)
	ctx.SSHConfigPath = sshConfigDeTeste(t, "")

	args := append(argumentosDeConexao(chave, porta, knownHosts),
		"-c", "/etc/nginx/nginx.conf", "inspect")

	var errBuf bytes.Buffer
	code := executar(NewRoot(ctx), ctx, args, &errBuf)
	require.NotEqual(t, output.ExitOK, code)

	env := envelopeDe(t, out)
	require.False(t, env.OK)
	require.NotEmpty(t, env.Diagnostics)

	var recusa string
	for _, d := range env.Diagnostics {
		if d.Severity == output.SeverityError {
			recusa = strings.ToLower(d.Message)
		}
	}
	// A asserção é sobre a mensagem NOSSA, e não sobre "permission denied":
	// a string do runtime muda entre sistemas e versoes de biblioteca, e um
	// consumidor que ramifique por ela quebra sozinho. O contrato e a causa
	// classificada.
	require.Contains(t, recusa, "permissao",
		"a recusa de leitura tem de aparecer; arvore vazia em silencio seria mentira")
	require.NotContains(t, recusa, "permission denied",
		"a string crua do runtime nao pode vazar para o diagnostico")
	require.Nil(t, env.Data, "sem leitura nao ha arvore: campo indisponivel e omitido")
}
