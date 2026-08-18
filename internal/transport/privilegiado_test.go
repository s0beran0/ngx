package transport_test

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/transport"
)

// fake answers reads and executions from canned data, and records what got
// executed. Proving that something was NOT escalated means looking at the
// commands, not just at the result.
type fake struct {
	arquivos   map[string]string
	negados    map[string]bool
	saidas     map[string]string
	falhas     map[string]bool
	executados [][]string
}

func novoFake() *fake {
	return &fake{
		arquivos: map[string]string{}, negados: map[string]bool{},
		saidas: map[string]string{}, falhas: map[string]bool{},
	}
}

func (f *fake) Open(p string) (io.ReadCloser, error) {
	if f.negados[p] {
		return nil, fs.ErrPermission
	}
	c, ok := f.arquivos[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(c)), nil
}

func (f *fake) Glob(padrao string) ([]string, error) {
	if f.negados[padrao] {
		return nil, fs.ErrPermission
	}
	return []string{}, nil
}

func (f *fake) Run(_ context.Context, argv []string) ([]byte, []byte, int, error) {
	f.executados = append(f.executados, argv)
	chave := ""
	for i, a := range argv {
		if i > 0 {
			chave += " "
		}
		chave += a
	}
	if f.falhas[chave] {
		return nil, []byte("sudo: a password is required"), 1, nil
	}
	return []byte(f.saidas[chave]), nil, 0, nil
}

func (f *fake) Close() error     { return nil }
func (f *fake) Describe() string { return "fake" }

// TestSemSudoNadaEEscalado is the half of the rule DR5 demands: with no flag,
// the transport has to come back exactly as it was, and no command may run. A
// test that only checked the path WITH sudo would let a silent escalation slip
// by unnoticed.
func TestSemSudoNadaEEscalado(t *testing.T) {
	f := novoFake()
	f.negados["/etc/nginx/nginx.conf"] = true

	tr := transport.ComLeituraPrivilegiada(context.Background(), f, false)
	_, err := tr.Open("/etc/nginx/nginx.conf")

	require.ErrorIs(t, err, fs.ErrPermission)
	assert.Empty(t, f.executados, "with no --sudo no command may run")
	assert.Same(t, transport.Transport(f), tr, "with no flag the transport comes back untouched")
}

// The escalation is MINIMAL: only the refused file is re-read with privilege.
// In a 132-file configuration where one is restricted, the other 131 must not
// go through sudo at all.
func TestComSudoSoORecusadoEElevado(t *testing.T) {
	f := novoFake()
	f.arquivos["/etc/nginx/aberto.conf"] = "worker_processes 1;\n"
	f.negados["/etc/nginx/restrito.conf"] = true
	f.saidas["sudo -n cat -- /etc/nginx/restrito.conf"] = "server { listen 80; }\n"

	tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)

	rc, err := tr.Open("/etc/nginx/aberto.conf")
	require.NoError(t, err)
	_ = rc.Close()
	assert.Empty(t, f.executados, "a readable file must not trigger sudo")

	rc, err = tr.Open("/etc/nginx/restrito.conf")
	require.NoError(t, err)
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, "server { listen 80; }\n", string(b))
	require.Len(t, f.executados, 1)
	assert.Equal(t,
		[]string{"sudo", "-n", "cat", "--", "/etc/nginx/restrito.conf"}, f.executados[0],
		"explicit argv, no shell: a file name must not turn into an injection")

	diags := transport.Diagnosticos(tr)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityInfo, diags[0].Severity)
	assert.Contains(t, diags[0].Message, "/etc/nginx/restrito.conf",
		"reading a server configuration with privilege cannot happen silently")
}

// A hardened server allows specific commands in sudoers -- typically nginx --
// and refuses `cat`. There the dump is the only way through, and without it
// privileged reading would be useless exactly where sudo is well configured.
func TestQuandoOSudoNaoPermiteCatODumpResolve(t *testing.T) {
	f := novoFake()
	f.negados["/etc/nginx/nginx.conf"] = true
	f.falhas["sudo -n cat -- /etc/nginx/nginx.conf"] = true

	dump := func(context.Context) (map[string][]byte, error) {
		return map[string][]byte{"/etc/nginx/nginx.conf": []byte("worker_processes 4;\n")}, nil
	}
	tr := transport.ComLeituraPrivilegiadaEDump(context.Background(), f, true, dump)

	rc, err := tr.Open("/etc/nginx/nginx.conf")
	require.NoError(t, err)
	b, _ := io.ReadAll(rc)
	_ = rc.Close()

	assert.Equal(t, "worker_processes 4;\n", string(b))
	diags := transport.Diagnosticos(tr)
	require.NotEmpty(t, diags)
	assert.Equal(t, transport.CodigoLeituraPeloDump, diags[0].Code,
		"the origin of the content has to show: it came from nginx -T, not from the file")
}

// With no dump and no `cat`, the outcome is a refusal with the reason — never a
// partial tree presented as complete.
func TestSemCaminhoNenhumRecusaEmVezDeApresentarParcial(t *testing.T) {
	f := novoFake()
	f.negados["/etc/nginx/nginx.conf"] = true
	f.falhas["sudo -n cat -- /etc/nginx/nginx.conf"] = true

	tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)
	_, err := tr.Open("/etc/nginx/nginx.conf")

	require.ErrorIs(t, err, fs.ErrPermission, "the cause is still permission")
	diags := transport.Diagnosticos(tr)
	require.Len(t, diags, 1)
	assert.Equal(t, output.SeverityError, diags[0].Severity)
	assert.Contains(t, diags[0].Message, "sudo")
}

// A path starting with `-` is the ARGUMENT injection case: without the `--`
// closing the options, cat would read "-rf" as a flag instead of as a file.
// Explicit argv solves shell injection and does not solve this one -- they are
// different defects, and the command here runs with privilege.
//
// The path comes from an `include` directive in the target's configuration,
// which is not trusted input.
func TestCaminhoComTracoNaoViraFlag(t *testing.T) {
	f := novoFake()
	suspeito := "/etc/nginx/-rf"
	f.negados[suspeito] = true
	f.saidas["sudo -n cat -- "+suspeito] = "worker_processes 1;\n"

	tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)
	rc, err := tr.Open(suspeito)
	require.NoError(t, err)
	_ = rc.Close()

	require.Len(t, f.executados, 1)
	argv := f.executados[0]
	require.Contains(t, argv, "--", "the end-of-options separator has to be there")
	assert.Less(t, indiceDe(argv, "--"), indiceDe(argv, suspeito),
		"the separator has to come BEFORE the path to be worth anything")
}

func indiceDe(lista []string, alvo string) int {
	for i, v := range lista {
		if v == alvo {
			return i
		}
	}
	return -1
}

// The trust tree is DERIVED from the configuration, never a fixed list of
// paths: a fixed list would break a non-standard installation, and a real
// server we measured includes from /etc/letsencrypt, outside /etc/nginx.
//
// The pair of cases is the point: elevating for a sibling of a file already
// reached is routine and comes out as info; elevating inside a directory the
// configuration had never touched is news, and news involving sudo comes out
// as a warning.
func TestElevacaoForaDaArvoreViraAviso(t *testing.T) {
	severidadeDe := func(diags []output.Diagnostic, codigo string) output.Severity {
		for _, d := range diags {
			if d.Code == codigo {
				return d.Severity
			}
		}
		return ""
	}

	t.Run("irmao de arquivo ja lido sai como info", func(t *testing.T) {
		f := novoFake()
		f.arquivos["/etc/nginx/nginx.conf"] = "include conf.d/*.conf;\n"
		f.negados["/etc/nginx/conf.d/restrito.conf"] = true
		f.saidas["sudo -n cat -- /etc/nginx/conf.d/restrito.conf"] = "server {}\n"

		tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)
		rc, err := tr.Open("/etc/nginx/nginx.conf")
		require.NoError(t, err)
		_ = rc.Close()
		rc, err = tr.Open("/etc/nginx/conf.d/restrito.conf")
		require.NoError(t, err)
		_ = rc.Close()

		diags := transport.Diagnosticos(tr)
		assert.Equal(t, output.SeverityInfo,
			severidadeDe(diags, transport.CodigoLeituraPrivilegiada))
		assert.Empty(t, severidadeDe(diags, transport.CodigoElevacaoForaDaArvore),
			"conf.d sits under /etc/nginx, which the configuration already reached")
	})

	t.Run("diretorio nunca tocado sai como aviso", func(t *testing.T) {
		f := novoFake()
		f.arquivos["/etc/nginx/nginx.conf"] = "include /opt/segredos/x.conf;\n"
		f.negados["/opt/segredos/x.conf"] = true
		f.saidas["sudo -n cat -- /opt/segredos/x.conf"] = "server {}\n"

		tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)
		rc, err := tr.Open("/etc/nginx/nginx.conf")
		require.NoError(t, err)
		_ = rc.Close()
		rc, err = tr.Open("/opt/segredos/x.conf")
		require.NoError(t, err)
		_ = rc.Close()

		diags := transport.Diagnosticos(tr)
		assert.Equal(t, output.SeverityWarning,
			severidadeDe(diags, transport.CodigoElevacaoForaDaArvore),
			"elevating in a new directory is the anomaly the warning exists to show")
	})

	t.Run("o proprio arquivo de topo nunca e anomalia", func(t *testing.T) {
		f := novoFake()
		f.negados["/opt/nginx-custom/nginx.conf"] = true
		f.saidas["sudo -n cat -- /opt/nginx-custom/nginx.conf"] = "events {}\n"

		tr := transport.ComLeituraPrivilegiada(context.Background(), f, true)
		rc, err := tr.Open("/opt/nginx-custom/nginx.conf")
		require.NoError(t, err)
		_ = rc.Close()

		diags := transport.Diagnosticos(tr)
		assert.Empty(t, severidadeDe(diags, transport.CodigoElevacaoForaDaArvore),
			"the configuration the operator named is not news, wherever it lives")
	})
}
