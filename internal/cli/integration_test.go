//go:build integration

// CLI integration through the remote path, against the bancada test bench
// (test/bancada).
//
// What is proven here is what only shows up at the end of the line: `inspect
// --host` returning the tree read from the container, the target in the
// envelope's meta, and the redaction of the three secrets before the output
// reaches whoever consumes it. The transport layer has its own tests in
// internal/transport.
//
// Behind the `integration` tag, and it SKIPS when the bancada is down.
// Run: make bancada-up && go test -tags integration ./... -race
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

	// The three secrets of the bancada, in the three shapes it reproduces.
	// They are the same as in test/bancada/gerar-config.sh.
	tokenDaBancada    = "Bearer ngx-bancada-token-4f3c9a1b2e"
	htpasswdDaBancada = "/etc/nginx/secrets/htpasswd"
	chaveTLSDaBancada = "/etc/nginx/secrets/tls.key"

	arquivoTopoRemoto  = "ngx-remoto.conf"
	dirConfDRemoto     = "etc/nginx/conf.d"
	arquivoDoContainer = "10-do-container.conf"
)

// The fixture lives in the HOME of the bancada user because /etc/nginx is
// 0700 root:root on purpose (the DR5 trap): as ngxtest, neither `nginx -T` nor
// the SFTP read reaches the real configuration. The relative wildcard is what
// competes with the file of the same name on the local disk.
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

// exigirBancada skips the test, instead of failing, when the bancada is down:
// whoever has no Docker cannot be shown a false failure.
func exigirBancada(t *testing.T) (chave string, porta int) {
	t.Helper()

	chave = filepath.Join(raizDoRepo(t), "test", "bancada", ".chave", "id_ed25519")
	porta = portaBancadaPadrao
	if valor := os.Getenv(envPortaBancada); valor != "" {
		var err error
		porta, err = strconv.Atoi(valor)
		require.NoErrorf(t, err, "%s=%q is not a port number", envPortaBancada, valor)
	}

	if _, err := os.Stat(chave); err != nil {
		t.Skipf("bancada is down: the test key %s does not exist. "+
			"Run `make bancada-up` (needs Docker).", chave)
	}

	endereco := net.JoinHostPort(hostBancada, strconv.Itoa(porta))
	conn, err := net.DialTimeout("tcp", endereco, 2*time.Second)
	if err != nil {
		t.Skipf("bancada is down: nothing is listening on %s (%v). "+
			"Run `make bancada-up` (needs Docker).", endereco, err)
	}
	_ = conn.Close()

	// Without the ssh-agent of whoever runs the suite: the bancada only
	// accepts the generated key, and an ssh-agent with several keys exhausts
	// the sshd's MaxAuthTries.
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

// knownHostsAprendido records the bancada's host key by learning it from the
// first-access refusal itself. No test here uses --insecure-host-key: the CLI
// connects with real verification.
func knownHostsAprendido(t *testing.T, chave string, porta int) string {
	t.Helper()

	caminho := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(caminho, nil, 0o600))

	tr, _, err := transport.SSHComDiagnosticos(opcoesDaBancada(chave, porta, caminho))
	if err == nil {
		_ = tr.Close()
		t.Fatal("the connection was accepted with an empty known_hosts")
	}

	var e *output.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, transport.CodigoHostDesconhecido, e.Diag.Code)

	const prefixo = "acrescente a linha ao arquivo: "
	_, linha, achou := strings.Cut(e.Diag.Message, prefixo)
	require.Truef(t, achou, "the message did not carry the known_hosts line: %s", e.Diag.Message)

	require.NoError(t, os.WriteFile(caminho, []byte(strings.TrimSpace(linha)+"\n"), 0o600))
	return caminho
}

// montarFixtureRemota writes the fixture into the container's HOME and removes
// it at the end. The `sh -c` is here, in the test, and not in ngx: ngx never
// assembles a shell line, and what it runs on the target remains explicit
// argv.
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
	require.Zerof(t, saida, "setting up the remote fixture failed: %s %s", stdout, stderr)

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

// arvoreDoEnvelope decodes only what this test observes of inspect's data.
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

// argumentosDeConexao assembles the bancada's remote access flags.
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

// The remote inspect end to end: the tree comes from the container, the target
// shows up in meta, and none of the three secrets crosses the output.
//
// The current directory is the glob trap's (test/bancada/armadilha-local),
// which holds a file with the same name as the container's. If the Glob were
// not injected into the parser, its marker would show up here — and ngx would
// be presenting files from the operator's machine as the server's
// configuration.
func TestInspectRemotoLeOContainerEnaoVazaSegredo(t *testing.T) {
	chave, porta := exigirBancada(t)
	knownHosts := knownHostsAprendido(t, chave, porta)
	montarFixtureRemota(t, chave, porta, knownHosts)

	armadilha := filepath.Join(raizDoRepo(t), "test", "bancada", "armadilha-local")
	t.Chdir(armadilha)

	// Control: on the local disk the same pattern matches the trap. Without
	// this proof, the test would pass vacuously in an empty directory.
	locais, err := transport.Local().Glob(dirConfDRemoto + "/*.conf")
	require.NoError(t, err)
	require.Contains(t, locais, filepath.Join(dirConfDRemoto, arquivoArmadilha),
		"the local trap is not in place")

	ctx, out := contextoDeTeste(t, nil)
	ctx.SSHConfigPath = sshConfigDeTeste(t, "")

	args := append(argumentosDeConexao(chave, porta, knownHosts),
		"-c", arquivoTopoRemoto, "inspect")

	var errBuf bytes.Buffer
	code := executar(NewRoot(ctx), ctx, args, &errBuf)
	require.Equalf(t, output.ExitOK, code, "stderr: %s\nstdout: %s", errBuf.String(), out.String())

	env := envelopeDe(t, out)
	require.True(t, env.OK)
	// The only expected diagnostic is the informational one about the missing
	// ssh-agent, which the test itself provokes by clearing SSH_AUTH_SOCK.
	for _, d := range env.Diagnostics {
		require.NotEqualf(t, output.SeverityError, d.Severity, "error diagnostic: %s", d.Message)
	}
	require.Equal(t, fmt.Sprintf("ssh://%s@%s:%d", usuarioBancada, hostBancada, porta), env.Meta.Target)
	require.NotEmpty(t, env.Meta.ConfigHash)

	// The tree is the container's: the file from the relative wildcard, plus
	// the real modules the absolute wildcard brought in.
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
	require.Positive(t, modulos, "the absolute wildcard brought in no module from the container")
	require.Equal(t, 2, data.Summary.Servers)

	bruto := out.String()
	require.NotContains(t, bruto, marcadorArmadilha,
		"the local disk configuration leaked into the tree read from the bancada")
	require.NotContains(t, bruto, arquivoArmadilha)

	// Redaction: none of the three shapes of secret goes out in the output,
	// and the directives stay visible — making the node disappear would make
	// the agent conclude the directive does not exist.
	for _, segredo := range []string{tokenDaBancada, htpasswdDaBancada, chaveTLSDaBancada} {
		require.NotContainsf(t, bruto, segredo, "the secret %q leaked into the output", segredo)
	}
	require.GreaterOrEqualf(t, strings.Count(bruto, output.RedactedValue), 3,
		"the three sensitive directives have to go out redacted, not omitted: %s", bruto)
	for _, diretiva := range []string{"proxy_set_header", "auth_basic_user_file", "ssl_certificate_key"} {
		require.Contains(t, bruto, diretiva)
	}
}

// The other half of DR5, on the reading path: as ngxtest, /etc/nginx is
// unreadable over SFTP. ngx reports the refusal instead of returning an empty
// tree — and does not retry the read with privilege on its own.
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
	// The assertion is about OUR message, and not about the raw runtime
	// string: the runtime string changes between systems and library
	// versions, and a consumer that branches on it breaks on its own. The
	// contract is the classified cause.
	require.Contains(t, recusa, "permissao",
		"the read refusal has to show up; a silently empty tree would be a lie")
	require.NotContains(t, recusa, "permission denied",
		"the raw runtime string cannot leak into the diagnostic")
	require.Nil(t, env.Data, "with no read there is no tree: an unavailable field is omitted")
}
