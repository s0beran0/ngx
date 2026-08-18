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
// "permission" in a message that became English, and every job stayed green.
//
// The pair of cases is the point. A permission failure gets the hint, because
// --sudo really does fix it; any other read failure must NOT, because
// suggesting --sudo for a dropped connection sends the operator down a road
// that leads nowhere.
func TestSudoHintOnlyComesOutForPermission(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantHint bool
	}{
		{"permission gets the hint", fs.ErrPermission, true},
		{"any other read failure does not", errors.New("connection reset by peer"), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			top := filepath.Join(dir, "nginx.conf")
			included := filepath.Join(dir, "denied.conf")
			require.NoError(t, os.WriteFile(top, []byte("include "+included+";\n"), 0o644))
			require.NoError(t, os.WriteFile(included, []byte("events {}\n"), 0o644))

			_, err := config.Parse(config.ParseOptions{
				Path: top,
				Open: func(p string) (io.ReadCloser, error) {
					if p == included {
						return nil, c.err
					}
					return os.Open(p)
				},
			})
			require.Error(t, err)

			ctx := &Context{Flags: &GlobalFlags{}}
			var problems config.ParseErrors
			require.True(t, errors.As(withSudoHint(err, ctx), &problems))

			hasHint := strings.Contains(problems[0].Message, "--sudo")
			require.Equal(t, c.wantHint, hasHint,
				"the --sudo hint only makes sense when --sudo actually fixes it")
		})
	}
}
