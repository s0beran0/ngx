package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// localTransport opera na maquina onde o ngx roda. Nao guarda estado:
// e um envelope fino sobre os/exec/filepath.
type localTransport struct{}

// Local devolve o transporte da maquina local.
func Local() Transport {
	return &localTransport{}
}

func (t *localTransport) Open(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

func (t *localTransport) Glob(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	if matches == nil {
		// Lista vazia, nunca nil: quem consome faz len() sem checar.
		return []string{}, nil
	}
	return matches, nil
}

func (t *localTransport) Run(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	if len(argv) == 0 {
		return nil, nil, 0, errors.New("transport: argv vazio")
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// O contexto vence sobre qualquer coisa: um processo morto por
	// cancelamento tambem devolve ExitError, mas isso e falha de
	// transporte, nao veredito do comando.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout.Bytes(), stderr.Bytes(), 0, ctxErr
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// O comando rodou e reprovou. Isso e resultado, nao erro.
			return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
		}
		// Binario inexistente, sem permissao de execucao, e afins.
		return stdout.Bytes(), stderr.Bytes(), 0, err
	}

	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

func (t *localTransport) Close() error {
	return nil
}

func (t *localTransport) Describe() string {
	return "local"
}
