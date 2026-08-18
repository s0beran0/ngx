package cli

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/config"
)

// The --sudo hint had no test, and that is exactly why translating the
// project silently removed it: the branch matched the Portuguese word
// "permissao" in a message that became English, and every job stayed green.
//
// The pair of cases is the point. A permission failure gets the hint, because
// --sudo really does fix it; any other read failure must NOT, because
// suggesting --sudo for a dropped connection sends the operator down a road
// that leads nowhere.
func TestDicaDeSudoSaiSoParaPermissao(t *testing.T) {
	casos := []struct {
		nome  string
		erro  error
		temNo bool
	}{
		{"permissao ganha a dica", fs.ErrPermission, true},
		{"outra falha de leitura nao ganha", errors.New("connection reset by peer"), false},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			dir := t.TempDir()
			topo := filepath.Join(dir, "nginx.conf")
			incluido := filepath.Join(dir, "negado.conf")
			require.NoError(t, os.WriteFile(topo, []byte("include "+incluido+";\n"), 0o644))
			require.NoError(t, os.WriteFile(incluido, []byte("events {}\n"), 0o644))

			_, err := config.Parse(config.ParseOptions{
				Path: topo,
				Open: func(p string) (io.ReadCloser, error) {
					if p == incluido {
						return nil, c.erro
					}
					return os.Open(p)
				},
			})
			require.Error(t, err)

			ctx := &Context{Flags: &GlobalFlags{}}
			var problemas config.ParseErrors
			require.True(t, errors.As(comDicaDeSudo(err, ctx), &problemas))

			temDica := strings.Contains(problemas[0].Message, "--sudo")
			require.Equal(t, c.temNo, temDica,
				"a dica de --sudo so faz sentido quando --sudo resolve")
		})
	}
}
