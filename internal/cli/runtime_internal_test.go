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

// Saidas gravadas de um nginx real. Nenhum teste deste arquivo executa nginx,
// abre socket ou toca o disco: o que esta em jogo e a fiacao entre o CLI e o
// runtime, e um teste que dependesse de um nginx instalado testaria o nginx.
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

// respostaGravada e o que um comando escreveu, congelado.
type respostaGravada struct {
	stdout string
	stderr string
	exit   int
}

// transporteGravado responde por argv exato e serve arquivos de memoria. E
// mais rico que o transporteFalso de remoto_internal_test.go, que devolve
// sempre a mesma saida: aqui um comando precisa responder diferente de
// outro, porque `status` executa dois.
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
		return nil, []byte("transporte de teste: argv nao gravado: " + strings.Join(argv, " ")), 127, nil
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

// rodarComTransporte executa o CLI contra um transporte gravado, entrando
// pelo caminho remoto (--host). E o caminho que prova as duas coisas ao mesmo
// tempo: que o comando executa o nginx pelo runtime e que ele o faz no alvo
// de --host, e nao na maquina de quem digitou.
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

// documentoUnico decodifica a saida e exige que ela seja um unico envelope.
// Um segundo documento JSON no stdout quebraria qualquer consumidor.
func documentoUnico(t *testing.T, out *bytes.Buffer) output.Envelope {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))

	var env output.Envelope
	require.NoError(t, dec.Decode(&env), "saida: %s", out.String())

	var sobra json.RawMessage
	require.ErrorIs(t, dec.Decode(&sobra), io.EOF,
		"o stdout precisa ter um unico envelope, e nao dois: %s", out.String())

	return env
}

// campos devolve o data do envelope como mapa, que e o unico jeito de afirmar
// que uma chave esta *ausente* — um struct de destino preencheria o zero
// value e a omissao passaria despercebida.
func campos(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	var resposta struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &resposta), "saida: %s", out.String())
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

// Configuracao reprovada e resultado, nao falha de infraestrutura: exit 3, e
// o envelope sai inteiro, com cada diagnostico no arquivo e na linha que o
// nginx informou.
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
		"a localizacao vira campo, nao fica so no texto")

	require.Equal(t, false, campos(t, out)["ok"])
}

// O binario que executa nao e mais um detalhe do plano: --nginx-bin chega ao
// argv, e --sudo prefixa o comando. Sem --sudo, nada de sudo — o ngx nao
// escala privilegio por conta propria (DR5).
func TestComandoTestHonraSudoENginxBin(t *testing.T) {
	t.Run("sem --sudo", func(t *testing.T) {
		tr := transporteComTesteOK()
		code, _ := rodarComTransporte(t, tr, "test")

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, [][]string{{"nginx", "-t"}}, tr.chamadas())
	})

	t.Run("com --sudo", func(t *testing.T) {
		tr := novoTransporteGravado().
			responde("sudo -n nginx -t", respostaGravada{stderr: saidaTesteOK})

		code, _ := rodarComTransporte(t, tr, "--sudo", "test")

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, [][]string{{"sudo", "-n", "nginx", "-t"}}, tr.chamadas())
	})

	t.Run("com --nginx-bin e --sudo", func(t *testing.T) {
		tr := novoTransporteGravado().
			responde("sudo -n /usr/local/sbin/nginx -t", respostaGravada{stderr: saidaTesteOK})

		code, _ := rodarComTransporte(t, tr,
			"--sudo", "--nginx-bin", "/usr/local/sbin/nginx", "test")

		require.Equal(t, output.ExitOK, code)
		require.Equal(t, [][]string{{"sudo", "-n", "/usr/local/sbin/nginx", "-t"}}, tr.chamadas())
	})
}

// Binario ausente e falha de infraestrutura, exit 1 — o oposto do exit 3 de
// configuracao reprovada. O envelope de erro sai com o codigo do runtime.
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

	// O `kill -0` nao leva sudo nem quando --sudo foi pedido em outro
	// comando: perguntar se um pid existe nao exige privilegio.
	require.Equal(t, [][]string{{"nginx", "-V"}, {"kill", "-0", "4242"}}, tr.chamadas())
}

// Pidfile ausente e evidencia, nao suposicao: o nginx apaga o arquivo ao
// parar. Aqui running sai false, com o diagnostico que diz por que.
func TestComandoStatusSemPidfileDizQueNaoEstaRodando(t *testing.T) {
	tr := novoTransporteGravado().
		responde("nginx -V", respostaGravada{stderr: saidaDetectOK})

	code, out := rodarComTransporte(t, tr, "status")

	require.Equal(t, output.ExitOK, code)

	env := documentoUnico(t, out)
	require.True(t, env.OK, "estado apurado nao derruba o comando")
	require.Len(t, env.Diagnostics, 1)
	require.Equal(t, "NGX-0225", env.Diagnostics[0].Code)

	processo := campos(t, out)["process"].(map[string]any)
	require.Equal(t, false, processo["running"])
	require.NotContains(t, processo, "master_pid")
}

// O que o nginx nao informa e omitido, nunca estimado. Um build sem
// --pid-path nao diz onde o pidfile fica, entao o ngx nao chuta um caminho:
// pid_file some, running some, e um diagnostico explica a ausencia. Reportar
// running false aqui diria que o nginx caiu.
func TestComandoStatusOmiteORunningQuandoNaoDaParaSaber(t *testing.T) {
	t.Run("build sem --pid-path", func(t *testing.T) {
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

	t.Run("pid de outro usuario", func(t *testing.T) {
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
			"sem evidencia, o campo sai — nunca vira false")
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

// Privilegio faltando e reportado, nunca contornado: o ngx nao repete o
// comando com sudo por conta propria, e o diagnostico diz qual comando o
// operador teria de autorizar.
func TestComandoStatusSemPrivilegioReportaEParaSemRepetirComSudo(t *testing.T) {
	tr := novoTransporteGravado().
		responde("nginx -V", respostaGravada{
			stderr: "nginx: [emerg] open() \"/etc/nginx/nginx.conf\" failed (13: Permission denied)",
			exit:   1,
		})

	code, out := rodarComTransporte(t, tr, "status")

	require.Equal(t, output.ExitInternal, code)
	require.Equal(t, [][]string{{"nginx", "-V"}}, tr.chamadas(),
		"o comando nao pode ser repetido com sudo por conta propria")

	env := documentoUnico(t, out)
	require.False(t, env.OK)
	require.Equal(t, "NGX-0221", env.Diagnostics[0].Code)
	require.Contains(t, env.Diagnostics[0].Message, "--sudo")
}

// Todo codigo de diagnostico do runtime que o CLI publica segue a secao 6.0
// do desenho: NGX- mais quatro digitos, sem letra e sem severidade embutida.
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

// A saida humana nao pode ser o JSON cru quando o dado sabe se apresentar.
func TestSaidaHumanaDosComandosDeRuntime(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, TestData{OK: true, ConfigFile: "/etc/nginx/nginx.conf"}.RenderHuman(&out))
	require.Equal(t, "configuracao aprovada: /etc/nginx/nginx.conf\n", out.String())

	rodando := true
	out.Reset()
	require.NoError(t, StatusData{
		Nginx:   nil,
		Process: ProcessData{Running: &rodando, MasterPID: 4242},
	}.RenderHuman(&out))
	require.Equal(t, "master 4242 rodando\n", out.String())

	out.Reset()
	require.NoError(t, StatusData{}.RenderHuman(&out))
	require.Equal(t, "estado do processo indisponivel\n", out.String(),
		"campo ausente vira frase ausente, nunca zero")
}
