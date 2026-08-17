package transport

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelperProcess nao e um teste: e o programa auxiliar que os testes de
// Run executam. Reexecutar o proprio binario de teste evita depender de
// utilitarios do sistema, que variam entre Linux, macOS e Windows, e nao
// exige shell.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("NGX_TRANSPORT_HELPER") != "1" {
		return
	}
	if out := os.Getenv("NGX_TRANSPORT_HELPER_STDOUT"); out != "" {
		fmt.Fprint(os.Stdout, out)
	}
	if errOut := os.Getenv("NGX_TRANSPORT_HELPER_STDERR"); errOut != "" {
		fmt.Fprint(os.Stderr, errOut)
	}
	code, _ := strconv.Atoi(os.Getenv("NGX_TRANSPORT_HELPER_EXIT"))
	os.Exit(code)
}

// helperArgv monta o argv que reexecuta este binario de teste em modo
// auxiliar, saindo com o codigo pedido.
func helperArgv(t *testing.T, exitCode int, stdout, stderr string) []string {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("NGX_TRANSPORT_HELPER", "1")
	t.Setenv("NGX_TRANSPORT_HELPER_EXIT", strconv.Itoa(exitCode))
	t.Setenv("NGX_TRANSPORT_HELPER_STDOUT", stdout)
	t.Setenv("NGX_TRANSPORT_HELPER_STDERR", stderr)
	return []string{self, "-test.run=^TestHelperProcess$"}
}

func TestLocalOpenArquivoExistente(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(path, []byte("worker_processes 1;\n"), 0o600))

	tr := Local()
	defer tr.Close()

	f, err := tr.Open(path)
	require.NoError(t, err)
	defer f.Close()

	conteudo, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, "worker_processes 1;\n", string(conteudo))
}

func TestLocalOpenArquivoInexistente(t *testing.T) {
	tr := Local()
	defer tr.Close()

	f, err := tr.Open(filepath.Join(t.TempDir(), "nao-existe.conf"))
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err), "erro deveria ser de arquivo inexistente, veio %v", err)
	assert.Nil(t, f)
}

func TestLocalGlobCasando(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.conf"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.conf"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.txt"), nil, 0o600))

	tr := Local()
	defer tr.Close()

	matches, err := tr.Glob(filepath.Join(dir, "*.conf"))
	require.NoError(t, err)
	assert.Equal(t, []string{
		filepath.Join(dir, "a.conf"),
		filepath.Join(dir, "b.conf"),
	}, matches)
}

func TestLocalGlobSemCorrespondencia(t *testing.T) {
	tr := Local()
	defer tr.Close()

	matches, err := tr.Glob(filepath.Join(t.TempDir(), "*.conf"))
	require.NoError(t, err)
	// Lista vazia, nunca nil: uma lista nula viraria "null" no JSON.
	assert.NotNil(t, matches)
	assert.Empty(t, matches)
}

func TestLocalRunSaidaZero(t *testing.T) {
	argv := helperArgv(t, 0, "tudo certo", "")

	tr := Local()
	defer tr.Close()

	stdout, stderr, exitCode, err := tr.Run(context.Background(), argv)
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "tudo certo", string(stdout))
	assert.Empty(t, string(stderr))
}

// TestLocalRunSaidaDiferenteDeZero e o teste que impede a inversao:
// codigo de saida diferente de zero e resultado do comando, com err nil.
// Se alguem transformar isso em erro, este teste falha.
func TestLocalRunSaidaDiferenteDeZero(t *testing.T) {
	argv := helperArgv(t, 3, "", "nginx: configuration file test failed")

	tr := Local()
	defer tr.Close()

	stdout, stderr, exitCode, err := tr.Run(context.Background(), argv)
	require.NoError(t, err, "codigo de saida diferente de zero e resultado, nao erro de transporte")
	assert.Equal(t, 3, exitCode)
	assert.Empty(t, string(stdout))
	assert.Equal(t, "nginx: configuration file test failed", string(stderr))
}

// TestLocalRunBinarioInexistente e a outra metade da distincao: aqui nao
// houve comando nenhum, entao err precisa ser nao nulo. Se alguem
// transformar erro de transporte em exitCode, este teste falha.
func TestLocalRunBinarioInexistente(t *testing.T) {
	tr := Local()
	defer tr.Close()

	argv := []string{filepath.Join(t.TempDir(), "binario-que-nao-existe"), "-t"}
	_, _, _, err := tr.Run(context.Background(), argv)
	require.Error(t, err, "binario inexistente e erro de transporte, nao veredito do comando")
}

func TestLocalRunArgvVazio(t *testing.T) {
	tr := Local()
	defer tr.Close()

	_, _, _, err := tr.Run(context.Background(), nil)
	require.Error(t, err)
}

// TestLocalRunContextoCancelado garante que cancelamento vira erro de
// transporte, e nao um codigo de saida qualquer: o processo morto por
// sinal tambem devolve ExitError.
func TestLocalRunContextoCancelado(t *testing.T) {
	argv := helperArgv(t, 0, "", "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tr := Local()
	defer tr.Close()

	_, _, exitCode, err := tr.Run(ctx, argv)
	require.Error(t, err)
	assert.Equal(t, 0, exitCode)
}

func TestLocalDescribe(t *testing.T) {
	assert.Equal(t, "local", Local().Describe())
}

func TestLocalCloseIdempotente(t *testing.T) {
	tr := Local()
	require.NoError(t, tr.Close())
	require.NoError(t, tr.Close())
}

// Guarda contra tempo infinito de suite caso o helper trave.
func TestMain(m *testing.M) {
	done := make(chan int, 1)
	go func() { done <- m.Run() }()
	select {
	case code := <-done:
		os.Exit(code)
	case <-time.After(60 * time.Second):
		fmt.Fprintln(os.Stderr, "transport: suite excedeu 60s")
		os.Exit(1)
	}
}
