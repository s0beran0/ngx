package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
)

func TestDetectExtraiCamposDoV(t *testing.T) {
	f := novoFake("local").responde("nginx -V", resposta{stderr: saidaVMenosMaiusculo})

	info, err := New(f).Detect(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "1.20.1", info.Version)
	assert.Empty(t, info.Flavor)
	assert.Equal(t, "/usr/share/nginx", info.Prefix)
	assert.Equal(t, "/etc/nginx/nginx.conf", info.MainConfig)
	assert.Equal(t, "/run/nginx.pid", info.PIDPath)
	assert.Equal(t, "/usr/lib64/nginx/modules", info.ModulesPath)
	assert.Contains(t, info.Modules, "http_ssl_module")
	assert.Contains(t, info.Modules, "http_v2_module")

	// A module built as dynamic is not a loaded module: only a load_module
	// in the tree answers that.
	assert.NotContains(t, info.Modules, "http_image_filter_module")
	assert.Contains(t, info.DynamicAvailable, "http_image_filter_module")

	// The quotes from --with-cc-opt must not leak into the module list.
	for _, m := range info.Modules {
		assert.NotContains(t, m, "'")
		assert.NotContains(t, m, "-flto")
	}
}

// The central guarantee of the task: the parser does not know where the bytes
// came from. Same recording through different transports, same result.
func TestDetectIdenticoLocalERemoto(t *testing.T) {
	local := novoFake("local").responde("nginx -V", resposta{stderr: saidaVMenosMaiusculo})
	remoto := novoFake("ssh://opc@10.0.0.7:22").responde("nginx -V", resposta{stderr: saidaVMenosMaiusculo})

	infoLocal, err := New(local).Detect(context.Background())
	require.NoError(t, err)
	infoRemoto, err := New(remoto).Detect(context.Background())
	require.NoError(t, err)

	assert.Equal(t, infoLocal, infoRemoto)
	assert.Equal(t, "local", New(local).Alvo())
	assert.Equal(t, "ssh://opc@10.0.0.7:22", New(remoto).Alvo())
}

// Some transports merge stdout and stderr. `-V` writes to stderr, but the
// parser has to work in both cases.
func TestDetectAceitaSaidaEmStdout(t *testing.T) {
	f := novoFake("local").responde("nginx -V", resposta{stdout: saidaVMenosMaiusculo})

	info, err := New(f).Detect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "1.20.1", info.Version)
}

func TestDetectResolveCaminhoRelativoAoPrefixo(t *testing.T) {
	f := novoFake("local").responde("nginx -V", resposta{stderr: saidaVCaminhoRelativo})

	info, err := New(f).Detect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/usr/local/nginx/conf/nginx.conf", info.MainConfig)
	assert.Equal(t, "/usr/local/nginx/logs/nginx.pid", info.PIDPath)
}

// An unavailable field is omitted, never estimated: a build that does not
// declare --pid-path does not get a guessed /run/nginx.pid.
func TestDetectOmiteCampoQueOVNaoInforma(t *testing.T) {
	f := novoFake("local").responde("nginx -V", resposta{
		stderr: "nginx version: nginx/1.27.0\nconfigure arguments: --prefix=/etc/nginx\n",
	})

	info, err := New(f).Detect(context.Background())
	require.NoError(t, err)
	assert.Empty(t, info.PIDPath)
	assert.Empty(t, info.MainConfig)
	assert.Empty(t, info.ModulesPath)

	bruto, err := json.Marshal(info)
	require.NoError(t, err)
	assert.NotContains(t, string(bruto), "pid_path")
	assert.NotContains(t, string(bruto), "main_config")

	// An empty list serializes as [], never as null.
	assert.Contains(t, string(bruto), `"modules":[]`)
	assert.Contains(t, string(bruto), `"dynamic_available":[]`)
}

func TestDetectVariante(t *testing.T) {
	f := novoFake("local").responde("nginx -V", resposta{
		stderr: "nginx version: openresty/1.21.4.1\n",
	})

	info, err := New(f).Detect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "openresty", info.Flavor)
	assert.Equal(t, "1.21.4.1", info.Version)
}

// A missing binary is a transport error in the local case: exec returns
// ErrNotFound without ever running anything.
func TestDetectNginxAusenteLocalmente(t *testing.T) {
	f := novoFake("local")
	f.padrao = &resposta{err: &exec.Error{Name: "nginx", Err: exec.ErrNotFound}}

	_, err := New(f).Detect(context.Background())

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodigoNginxAusente, e.Diag.Code)
	assert.True(t, errors.Is(err, exec.ErrNotFound))
}

// In the remote case the same outcome arrives as exit code 127 from the
// target's shell, with no transport error at all. Both have to become the same
// diagnostic.
func TestDetectNginxAusenteRemotamente(t *testing.T) {
	f := novoFake("ssh://opc@10.0.0.7:22").responde("nginx -V", resposta{
		stderr: "bash: nginx: command not found\n",
		exit:   127,
	})

	_, err := New(f).Detect(context.Background())

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodigoNginxAusente, e.Diag.Code)
	assert.Contains(t, e.Diag.Message, "ssh://opc@10.0.0.7:22")
}

func TestDetectSaidaNaoReconhecida(t *testing.T) {
	f := novoFake("local").responde("nginx -V", resposta{stderr: "something else entirely\n"})

	_, err := New(f).Detect(context.Background())

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodigoSaidaNaoReconhecida, e.Diag.Code)
}

func TestDetectUsaBinarioInformado(t *testing.T) {
	f := novoFake("local").responde("/opt/nginx/sbin/nginx -V", resposta{stderr: saidaVMenosMaiusculo})

	info, err := New(f, ComBinario("/opt/nginx/sbin/nginx")).Detect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/opt/nginx/sbin/nginx", info.Binary)
}

func TestDividirArgumentosRespeitaAspas(t *testing.T) {
	args := dividirArgumentos(`--prefix=/etc --with-cc-opt='-O2 -g' --with-http_ssl_module`)
	assert.Equal(t, []string{"--prefix=/etc", "--with-cc-opt=-O2 -g", "--with-http_ssl_module"}, args)
	assert.Equal(t, []string{}, dividirArgumentos(""))
}
