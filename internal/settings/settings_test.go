package settings_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/settings"
	"github.com/stretchr/testify/require"
)

func escreve(t *testing.T, dir, nome, conteudo string) string {
	t.Helper()
	p := filepath.Join(dir, nome)
	require.NoError(t, os.WriteFile(p, []byte(conteudo), 0o644))
	return p
}

// With no file at all, the defaults need to be usable on their own.
func TestLoadSemArquivosUsaDefaults(t *testing.T) {
	dir := t.TempDir()

	s, err := settings.Load(filepath.Join(dir, "global.yaml"), filepath.Join(dir, "local.yaml"))

	require.NoError(t, err)
	require.Equal(t, "auto", s.Output.Format)
	require.NotEmpty(t, s.Output.Redact, "redaction comes turned on by default")
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

// The rule from the spec: the local file overrides the global one, key by key.
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
	require.Equal(t, "/tmp/teste/nginx.conf", s.Nginx.Config, "the local file wins")
	require.Equal(t, "/usr/sbin/nginx", s.Nginx.Binary, "a key that was not overridden survives")
	require.Equal(t, "json", s.Output.Format)
}

// A file written from the complete spec contains keys from future versions.
// They need to be ignored, not turned into an error.
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

// If the user declares redact, their list replaces the default one instead of
// adding to it -- otherwise they cannot remove a default rule.
func TestRedactDeclaradoSubstituiODefault(t *testing.T) {
	dir := t.TempDir()
	global := escreve(t, dir, "global.yaml", `
output:
  redact:
    - my_secret_directive
`)

	s, err := settings.Load(global, filepath.Join(dir, "ausente.yaml"))

	require.NoError(t, err)
	require.Equal(t, []string{"my_secret_directive"}, s.Output.Redact)
}

// redact: [] is an empty list declared on purpose: the user decided to turn
// redaction off, and that must be respected.
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

// redact: with no value is a YAML null, typical of a file where the person
// commented out every item of the list. That cannot be confused with a
// declared empty list: the defaults need to survive, otherwise redaction turns
// off silently on a security feature.
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

// Defaults() is a public contract consumed by Task 6; the three redact values
// are an explicit part of that contract and need to be pinned.
func TestDefaults(t *testing.T) {
	d := settings.Defaults()
	require.Equal(t, "auto", d.Output.Format)
	require.Equal(t, []string{
		"ssl_certificate_key",
		"proxy_set_header Authorization",
		"auth_basic_user_file",
	}, d.Output.Redact)
}
