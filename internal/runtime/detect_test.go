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

	// Modulo construido como dinamico nao e modulo carregado: so um
	// load_module na arvore responde isso.
	assert.NotContains(t, info.Modules, "http_image_filter_module")
	assert.Contains(t, info.DynamicAvailable, "http_image_filter_module")

	// As aspas do --with-cc-opt nao podem vazar para a lista de modulos.
	for _, m := range info.Modules {
		assert.NotContains(t, m, "'")
		assert.NotContains(t, m, "-flto")
	}
}

// A garantia central da tarefa: o parser nao sabe de onde os bytes vieram.
// Mesma gravacao por transportes diferentes, mesmo resultado.
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

// Alguns transportes juntam stdout e stderr. O `-V` escreve em stderr, mas o
// parser tem de funcionar nos dois casos.
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

// Campo indisponivel e omitido, nunca estimado: um build que nao declara
// --pid-path nao ganha um /run/nginx.pid chutado.
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

	// Lista vazia serializa como [], nunca null.
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

// Binario ausente e erro de transporte no caso local: exec devolve
// ErrNotFound sem nunca rodar nada.
func TestDetectNginxAusenteLocalmente(t *testing.T) {
	f := novoFake("local")
	f.padrao = &resposta{err: &exec.Error{Name: "nginx", Err: exec.ErrNotFound}}

	_, err := New(f).Detect(context.Background())

	var e *output.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, CodigoNginxAusente, e.Diag.Code)
	assert.True(t, errors.Is(err, exec.ErrNotFound))
}

// No caso remoto o mesmo desfecho chega como codigo 127 do shell do alvo, sem
// erro de transporte nenhum. Os dois tem de virar o mesmo diagnostico.
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
	f := novoFake("local").responde("nginx -V", resposta{stderr: "outra coisa qualquer\n"})

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
