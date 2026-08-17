package settings_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eduardoborges/ngx/internal/settings"
	"github.com/stretchr/testify/require"
)

func escreve(t *testing.T, dir, nome, conteudo string) string {
	t.Helper()
	p := filepath.Join(dir, nome)
	require.NoError(t, os.WriteFile(p, []byte(conteudo), 0o644))
	return p
}

// Sem nenhum arquivo, os defaults precisam ser utilizaveis por conta propria.
func TestLoadSemArquivosUsaDefaults(t *testing.T) {
	dir := t.TempDir()

	s, err := settings.Load(filepath.Join(dir, "global.yaml"), filepath.Join(dir, "local.yaml"))

	require.NoError(t, err)
	require.Equal(t, "auto", s.Output.Format)
	require.NotEmpty(t, s.Output.Redact, "a redacao vem ligada por padrao")
	require.Contains(t, s.Output.Redact, "ssl_certificate_key")
}

func TestLoadLeArquivoGlobal(t *testing.T) {
	dir := t.TempDir()
	global := escreve(t, dir, "global.yaml", `
nginx:
  binary: /usr/sbin/nginx
  config: /etc/nginx/nginx.conf
output:
  format: json
`)

	s, err := settings.Load(global, filepath.Join(dir, "ausente.yaml"))

	require.NoError(t, err)
	require.Equal(t, "/usr/sbin/nginx", s.Nginx.Binary)
	require.Equal(t, "json", s.Output.Format)
}

// A regra da spec: o local sobrescreve o global, chave a chave.
func TestLocalSobrescreveGlobal(t *testing.T) {
	dir := t.TempDir()
	global := escreve(t, dir, "global.yaml", `
nginx:
  binary: /usr/sbin/nginx
  config: /etc/nginx/nginx.conf
output:
  format: json
`)
	local := escreve(t, dir, "local.yaml", `
nginx:
  config: /tmp/teste/nginx.conf
`)

	s, err := settings.Load(global, local)

	require.NoError(t, err)
	require.Equal(t, "/tmp/teste/nginx.conf", s.Nginx.Config, "local vence")
	require.Equal(t, "/usr/sbin/nginx", s.Nginx.Binary, "chave nao sobrescrita sobrevive")
	require.Equal(t, "json", s.Output.Format)
}

// Um arquivo escrito a partir da spec completa contem chaves de versoes
// futuras. Elas precisam ser ignoradas, nao virar erro.
func TestChavesDeVersoesFuturasSaoIgnoradas(t *testing.T) {
	dir := t.TempDir()
	global := escreve(t, dir, "global.yaml", `
nginx:
  binary: /usr/sbin/nginx
apply:
  require_plan: true
  guardrails:
    block_listen_removal: true
snapshot:
  backend: fs
lint:
  fail_on: high
mcp:
  transport: stdio
`)

	s, err := settings.Load(global, filepath.Join(dir, "ausente.yaml"))

	require.NoError(t, err)
	require.Equal(t, "/usr/sbin/nginx", s.Nginx.Binary)
}

func TestYAMLInvalidoVirarErro(t *testing.T) {
	dir := t.TempDir()
	ruim := escreve(t, dir, "ruim.yaml", "nginx: [isto: nao: fecha")

	_, err := settings.Load(ruim, filepath.Join(dir, "ausente.yaml"))

	require.Error(t, err)
}

// Se o usuario declara redact, a lista dele substitui a default em vez de
// somar — senao ele nao consegue remover uma regra padrao.
func TestRedactDeclaradoSubstituiODefault(t *testing.T) {
	dir := t.TempDir()
	global := escreve(t, dir, "global.yaml", `
output:
  redact:
    - minha_diretiva_secreta
`)

	s, err := settings.Load(global, filepath.Join(dir, "ausente.yaml"))

	require.NoError(t, err)
	require.Equal(t, []string{"minha_diretiva_secreta"}, s.Output.Redact)
}

// redact: [] e uma lista vazia declarada de proposito: o usuario decidiu
// desligar a redacao, e isso deve ser respeitado.
func TestRedactListaVaziaDesligaARedacao(t *testing.T) {
	dir := t.TempDir()
	global := escreve(t, dir, "global.yaml", `
output:
  redact: []
`)

	s, err := settings.Load(global, filepath.Join(dir, "ausente.yaml"))

	require.NoError(t, err)
	require.Empty(t, s.Output.Redact)
}

// redact: sem valor e um YAML nulo, tipico de um arquivo onde a pessoa
// comentou todos os itens da lista. Isso nao pode ser confundido com uma
// lista vazia declarada: os defaults precisam sobreviver, senao a redacao
// desliga em silencio numa feature de seguranca.
func TestRedactNuloPreservaOsDefaults(t *testing.T) {
	dir := t.TempDir()
	global := escreve(t, dir, "global.yaml", `
output:
  redact:
`)

	s, err := settings.Load(global, filepath.Join(dir, "ausente.yaml"))

	require.NoError(t, err)
	require.Equal(t, []string{
		"ssl_certificate_key",
		"proxy_set_header Authorization",
		"auth_basic_user_file",
	}, s.Output.Redact)
}

// Defaults() e contrato publico consumido pela Task 6; os tres valores de
// redact sao parte explicita desse contrato e precisam estar travados.
func TestDefaults(t *testing.T) {
	d := settings.Defaults()
	require.Equal(t, "auto", d.Output.Format)
	require.Equal(t, []string{
		"ssl_certificate_key",
		"proxy_set_header Authorization",
		"auth_basic_user_file",
	}, d.Output.Redact)
}
