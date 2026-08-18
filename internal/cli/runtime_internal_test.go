package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/transport"
	"github.com/stretchr/testify/require"
)

// Recorded outputs from a real nginx. No test in this file runs nginx, opens
// a socket or touches the disk: what is at stake is the wiring between the CLI
// and the runtime, and a test that depended on an installed nginx would be
// testing nginx.
const (
	saidaDetectOK = `nginx version: nginx/1.24.0
built by gcc 12.2.0 (Debian 12.2.0-14)
built with OpenSSL 3.0.11 19 Sep 2023
TLS SNI support enabled
configure arguments: --prefix=/etc/nginx --conf-path=/etc/nginx/nginx.conf --pid-path=/var/run/nginx.pid --with-http_ssl_module --with-http_geoip_module=dynamic
`

	saidaDetectSemPIDPath = `nginx version: nginx/1.24.0
configure arguments: --prefix=/etc/nginx --conf-path=/etc/nginx/nginx.conf --with-http_ssl_module
`

	saidaTesteOK = `nginx: the configuration file /etc/nginx/nginx.conf syntax is ok
nginx: configuration file /etc/nginx/nginx.conf test is successful
`

	saidaTesteReprovado = `nginx: [warn] conflicting server name "app.local" on 0.0.0.0:80, ignored
nginx: [emerg] invalid number of arguments in "listen" directive in /etc/nginx/conf.d/app.conf:12
nginx: configuration file /etc/nginx/nginx.conf test failed
`

	pidfilePadrao = "/var/run/nginx.pid"
)

// respostaGravada is what a command wrote, frozen.
type respostaGravada struct {
	stdout string
	stderr string
	exit   int
}

// transporteGravado answers by exact argv and serves files from memory. It is
// richer than the transporteFalso of remoto_internal_test.go, which always
// returns the same output: here one command has to answer differently from
// another, because `status` runs two of them.
type transporteGravado struct {
	descricao string
	respostas map[string]respostaGravada
	arquivos  map[string]string
	errosOpen map[string]error

	mu         sync.Mutex
	executados [][]string
}

func novoTransporteGravado() *transporteGravado {
	return &transporteGravado{
		descricao: "ssh://deploy@10.0.0.9:22",
		respostas: map[string]respostaGravada{},
		arquivos:  map[string]string{},
		errosOpen: map[string]error{},
	}
}

func (t *transporteGravado) responde(argv string, r respostaGravada) *transporteGravado {
	t.respostas[argv] = r
	return t
}

func (t *transporteGravado) Open(caminho string) (io.ReadCloser, error) {
	if err, ok := t.errosOpen[caminho]; ok {
		return nil, err
	}
	if conteudo, ok := t.arquivos[caminho]; ok {
		return io.NopCloser(strings.NewReader(conteudo)), nil
	}
	return nil, &fs.PathError{Op: "open", Path: caminho, Err: fs.ErrNotExist}
}

func (t *transporteGravado) Glob(string) ([]string, error) { return []string{}, nil }

func (t *transporteGravado) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	t.mu.Lock()
	t.executados = append(t.executados, append([]string(nil), argv...))
	t.mu.Unlock()

	r, ok := t.respostas[strings.Join(argv, " ")]
	if !ok {
		return nil, []byte("test transport: argv not recorded: " + strings.Join(argv, " ")), 127, nil
	}
	return []byte(r.stdout), []byte(r.stderr), r.exit, nil
}

func (t *transporteGravado) Close() error { return nil }

func (t *transporteGravado) Describe() string { return t.descricao }

func (t *transporteGravado) chamadas() [][]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([][]string(nil), t.executados...)
}

// rodarComTransporte runs the CLI against a recorded transport, entering
// through the remote path (--host). It is the path that proves two things at
// once: that the command runs nginx through the runtime and that it does so on
// the --host target, and not on the machine of whoever typed it.
func rodarComTransporte(t *testing.T, tr *transporteGravado, args ...string) (output.ExitCode, *bytes.Buffer) {
	t.Helper()

	ctx, out := contextoDeTeste(t, nil)
	ctx.SSHConfigPath = sshConfigDeTeste(t, "")
	ctx.ConectarSSH = func(transport.SSHOptions) (transport.Transport, []output.Diagnostic, error) {
		return tr, nil, nil
	}

	var errBuf bytes.Buffer
	completos := append([]string{"--host", "10.0.0.9"}, args...)
	return executar(NewRoot(ctx), ctx, completos, &errBuf), out
}

// documentoUnico decodes the output and requires it to be a single envelope.
// A second JSON document on stdout would break any consumer.
func documentoUnico(t *testing.T, out *bytes.Buffer) output.Envelope {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))

	var env output.Envelope
	require.NoError(t, dec.Decode(&env), "output: %s", out.String())

	var sobra json.RawMessage
	require.ErrorIs(t, dec.Decode(&sobra), io.EOF,
		"stdout has to hold a single envelope, and not two: %s", out.String())

	return env
}

// campos returns the envelope's data as a map, which is the only way to assert
// that a key is *absent* — a destination struct would fill in the zero value
// and the omission would go unnoticed.
func campos(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	var resposta struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &resposta), "output: %s", out.String())
	return resposta.Data
}

func transporteComTesteOK() *transporteGravado {
	return novoTransporteGravado().
		responde("nginx -t", respostaGravada{stderr: saidaTesteOK})
}

func TestComandoTestExecutaNginxNoAlvoEDevolveOEnvelope(t *testing.T) {
	tr := transporteComTesteOK()

	code, out := rodarComTransporte(t, tr, "test")

	require.Equal(t, output.ExitOK, code)
	require.Equal(t, [][]string{{"nginx", "-t"}}, tr.chamadas())

	env := documentoUnico(t, out)
	require.True(t, env.OK)
	require.Equal(t, "test", env.Command)
	require.Equal(t, "ssh://deploy@10.0.0.9:22", env.Meta.Target)
	require.Empty(t, env.Diagnostics)

	data := campos(t, out)
	require.Equal(t, true, data["ok"])
	require.Equal(t, "/etc/nginx/nginx.conf", data["config_file"])
}

// A rejected configuration is a result, not an infrastructure failure: exit 3,
// and the envelope goes out whole, with each diagnostic on the file and line
// nginx reported.
func TestComandoTestComConfiguracaoReprovadaEhExit3ComDiagnosticoLocalizado(t *testing.T) {
	tr := novoTransporteGravado().
		responde("nginx -t", respostaGravada{stderr: saidaTesteReprovado, exit: 1})

	code, out := rodarComTransporte(t, tr, "test")

	require.Equal(t, output.ExitInvalidConfig, code)

	env := documentoUnico(t, out)
	require.False(t, env.OK)
	require.Equal(t, "test", env.Command)
	require.Len(t, env.Diagnostics, 2)

	require.Equal(t, output.SeverityWarning, env.Diagnostics[0].Severity)

	emerg := env.Diagnostics[1]
	require.Equal(t, output.SeverityError, emerg.Severity)
	require.Equal(t, "NGX-0224", emerg.Code)
	require.Equal(t, "/etc/nginx/conf.d/app.conf", emerg.File)
	require.Equal(t, 12, emerg.Line)
	require.NotContains(t, emerg.Message, "app.conf:12",
		"the location becomes a field, it does not stay only in the text")

	require.Equal(t, false, campos(t, out)["ok"])
}

// The binary that runs is no longer a detail of the plan: --nginx-bin reaches
// the argv, and --sudo prefixes the command. Without --sudo, no sudo — ngx
// does not escalate privilege on its own (DR5).
func TestComandoTestHonraSudoENginxBin(t *testing.T) {
	t.Run("without --sudo", func(t *testing.T) {
		tr := transporteComTesteOK()
		code, _ := rodarComTransporte(t, tr, "test")

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, [][]string{{"nginx", "-t"}}, tr.chamadas())
	})

	t.Run("with --sudo", func(t *testing.T) {
		tr := novoTransporteGravado().
			responde("sudo -n nginx -t", respostaGravada{stderr: saidaTesteOK})

		code, _ := rodarComTransporte(t, tr, "--sudo", "test")

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, [][]string{{"sudo", "-n", "nginx", "-t"}}, tr.chamadas())
	})

	t.Run("with --nginx-bin and --sudo", func(t *testing.T) {
		tr := novoTransporteGravado().
			responde("sudo -n /usr/local/sbin/nginx -t", respostaGravada{stderr: saidaTesteOK})

		code, _ := rodarComTransporte(t, tr,
			"--sudo", "--nginx-bin", "/usr/local/sbin/nginx", "test")

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, [][]string{{"sudo", "-n", "/usr/local/sbin/nginx", "-t"}}, tr.chamadas())
	})
}

// A missing binary is an infrastructure failure, exit 1 — the opposite of the
// exit 3 of a rejected configuration. The error envelope goes out with the
// runtime's code.
func TestComandoTestSemNginxNoAlvoEhFalhaInterna(t *testing.T) {
	tr := novoTransporteGravado().
		responde("nginx -t", respostaGravada{stderr: "bash: nginx: command not found", exit: 127})

	code, out := rodarComTransporte(t, tr, "test")

	require.Equal(t, output.ExitInternal, code)

	env := documentoUnico(t, out)
	require.False(t, env.OK)
	require.Equal(t, "NGX-0220", env.Diagnostics[0].Code)
}

func transporteComStatusVivo() *transporteGravado {
	tr := novoTransporteGravado().
		responde("nginx -V", respostaGravada{stderr: saidaDetectOK}).
		responde("kill -0 4242", respostaGravada{})
	tr.arquivos[pidfilePadrao] = "4242\n"
	return tr
}

func TestComandoStatusJuntaDeteccaoEEstadoDoProcesso(t *testing.T) {
	tr := transporteComStatusVivo()

	code, out := rodarComTransporte(t, tr, "status")

	require.Equal(t, output.ExitOK, code)

	env := documentoUnico(t, out)
	require.True(t, env.OK)
	require.Equal(t, "status", env.Command)
	require.Equal(t, "ssh://deploy@10.0.0.9:22", env.Meta.Target)
	require.Equal(t, "1.24.0", env.Meta.NginxVersion)

	var resposta struct {
		Data StatusData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &resposta))

	require.Equal(t, "1.24.0", resposta.Data.Nginx.Version)
	require.Equal(t, "nginx", resposta.Data.Nginx.Binary)
	require.Equal(t, "/etc/nginx/nginx.conf", resposta.Data.Nginx.MainConfig)
	require.Contains(t, resposta.Data.Nginx.Modules, "http_ssl_module")
	require.Contains(t, resposta.Data.Nginx.DynamicAvailable, "http_geoip_module")

	require.NotNil(t, resposta.Data.Process.Running)
	require.True(t, *resposta.Data.Process.Running)
	require.Equal(t, 4242, resposta.Data.Process.MasterPID)
	require.Equal(t, pidfilePadrao, resposta.Data.Process.PIDFile)

	// The `kill -0` does not take sudo even when --sudo was asked for on
	// another command: asking whether a pid exists requires no privilege.
	require.Equal(t, [][]string{{"nginx", "-V"}, {"kill", "-0", "4242"}}, tr.chamadas())
}

// A missing pidfile is evidence, not an assumption: nginx deletes the file
// when it stops. Here running goes out false, with the diagnostic saying why.
func TestComandoStatusSemPidfileDizQueNaoEstaRodando(t *testing.T) {
	tr := novoTransporteGravado().
		responde("nginx -V", respostaGravada{stderr: saidaDetectOK})

	code, out := rodarComTransporte(t, tr, "status")

	require.Equal(t, output.ExitOK, code)

	env := documentoUnico(t, out)
	require.True(t, env.OK, "a determined state does not bring the command down")
	require.Len(t, env.Diagnostics, 1)
	require.Equal(t, "NGX-0225", env.Diagnostics[0].Code)

	processo := campos(t, out)["process"].(map[string]any)
	require.Equal(t, false, processo["running"])
	require.NotContains(t, processo, "master_pid")
}

// What nginx does not report is omitted, never estimated. A build without
// --pid-path does not say where the pidfile lives, so ngx does not guess a
// path: pid_file goes away, running goes away, and a diagnostic explains the
// absence. Reporting running false here would say nginx went down.
func TestComandoStatusOmiteORunningQuandoNaoDaParaSaber(t *testing.T) {
	t.Run("build without --pid-path", func(t *testing.T) {
		tr := novoTransporteGravado().
			responde("nginx -V", respostaGravada{stderr: saidaDetectSemPIDPath})

		code, out := rodarComTransporte(t, tr, "status")

		require.Equal(t, output.ExitOK, code)

		env := documentoUnico(t, out)
		require.True(t, env.OK)
		require.Len(t, env.Diagnostics, 1)
		require.Equal(t, output.SeverityWarning, env.Diagnostics[0].Severity)
		require.Equal(t, "NGX-0225", env.Diagnostics[0].Code)

		data := campos(t, out)
		require.NotContains(t, data["nginx"], "pid_path")

		processo := data["process"].(map[string]any)
		require.NotContains(t, processo, "running")
		require.NotContains(t, processo, "pid_file")
	})

	t.Run("pid of another user", func(t *testing.T) {
		tr := novoTransporteGravado().
			responde("nginx -V", respostaGravada{stderr: saidaDetectOK}).
			responde("kill -0 4242", respostaGravada{
				stderr: "kill: (4242) - Operation not permitted",
				exit:   1,
			})
		tr.arquivos[pidfilePadrao] = "4242\n"

		code, out := rodarComTransporte(t, tr, "status")

		require.Equal(t, output.ExitOK, code)

		env := documentoUnico(t, out)
		require.Len(t, env.Diagnostics, 1)
		require.Equal(t, "NGX-0221", env.Diagnostics[0].Code)

		processo := campos(t, out)["process"].(map[string]any)
		require.NotContains(t, processo, "running",
			"with no evidence the field goes away — it never becomes false")
		require.Equal(t, float64(4242), processo["master_pid"])
	})
}

func TestComandoStatusHonraSudoENginxBin(t *testing.T) {
	tr := novoTransporteGravado().
		responde("sudo -n /usr/local/sbin/nginx -V", respostaGravada{stderr: saidaDetectOK}).
		responde("kill -0 4242", respostaGravada{})
	tr.arquivos[pidfilePadrao] = "4242\n"

	code, out := rodarComTransporte(t, tr,
		"--sudo", "--nginx-bin", "/usr/local/sbin/nginx", "status")

	require.Equal(t, output.ExitOK, code)
	require.Equal(t, [][]string{
		{"sudo", "-n", "/usr/local/sbin/nginx", "-V"},
		{"kill", "-0", "4242"},
	}, tr.chamadas())

	var resposta struct {
		Data StatusData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &resposta))
	require.Equal(t, "/usr/local/sbin/nginx", resposta.Data.Nginx.Binary)
}

// Missing privilege is reported, never worked around: ngx does not retry the
// command with sudo on its own, and the diagnostic says which command the
// operator would have to authorize.
func TestComandoStatusSemPrivilegioReportaEParaSemRepetirComSudo(t *testing.T) {
	tr := novoTransporteGravado().
		responde("nginx -V", respostaGravada{
			stderr: "nginx: [emerg] open() \"/etc/nginx/nginx.conf\" failed (13: Permission denied)",
			exit:   1,
		})

	code, out := rodarComTransporte(t, tr, "status")

	require.Equal(t, output.ExitInternal, code)
	require.Equal(t, [][]string{{"nginx", "-V"}}, tr.chamadas(),
		"the command cannot be retried with sudo on its own")

	env := documentoUnico(t, out)
	require.False(t, env.OK)
	require.Equal(t, "NGX-0221", env.Diagnostics[0].Code)
	require.Contains(t, env.Diagnostics[0].Message, "--sudo")
}

// Every runtime diagnostic code the CLI publishes follows section 6.0 of the
// design: NGX- plus four digits, with no letter and no embedded severity.
func TestCodigosDeDiagnosticoDoRuntimeSeguemOFormatoDaSpec(t *testing.T) {
	tr := novoTransporteGravado().
		responde("nginx -t", respostaGravada{stderr: saidaTesteReprovado, exit: 1})

	_, out := rodarComTransporte(t, tr, "test")

	env := documentoUnico(t, out)
	require.NotEmpty(t, env.Diagnostics)
	for _, d := range env.Diagnostics {
		require.Regexp(t, `^NGX-\d{4}$`, d.Code)
	}
}

// The human output cannot be the raw JSON when the data knows how to present
// itself.
func TestSaidaHumanaDosComandosDeRuntime(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, TestData{OK: true, ConfigFile: "/etc/nginx/nginx.conf"}.RenderHuman(&out))
	require.Equal(t, "configuration accepted: /etc/nginx/nginx.conf\n", out.String())

	rodando := true
	out.Reset()
	require.NoError(t, StatusData{
		Nginx:   nil,
		Process: ProcessData{Running: &rodando, MasterPID: 4242},
	}.RenderHuman(&out))
	require.Equal(t, "master 4242 running\n", out.String())

	out.Reset()
	require.NoError(t, StatusData{}.RenderHuman(&out))
	require.Equal(t, "process state unavailable\n", out.String(),
		"an absent field becomes an absent sentence, never a zero")
}
