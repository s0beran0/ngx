//go:build integration

package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// nginx is the oracle for what nginx accepts. Our reading of the documentation
// is not, and this was not a theoretical concern while writing the fixture:
// three constructions that looked valid were rejected by the real binary, one
// of them because `\$` does not escape a variable the way a reader of the
// escaping rules would assume.
//
// So the fixture is checked against a real nginx on every integration run. If
// somebody adds a construction we cannot parse, the unit test catches it. If
// somebody adds one nginx does not accept, this catches it -- and without this,
// the unit test would happily lock in a fixture nginx would refuse.
func TestTheSyntaxSurfaceIsAcceptedByRealNginx(t *testing.T) {
	caminho, err := filepath.Abs(filepath.Join("testdata", "syntax_surface.conf"))
	require.NoError(t, err)

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available; bring the bench up to run this")
	}
	if err := exec.Command("docker", "inspect", "ngx-bench").Run(); err != nil {
		t.Skip("the bench is not up: run `make bench-up`")
	}

	dados, err := os.ReadFile(caminho)
	require.NoError(t, err)

	copiar := exec.Command("docker", "exec", "-i", "ngx-bench",
		"sh", "-c", "cat > /tmp/syntax_surface.conf")
	copiar.Stdin = strings.NewReader(string(dados))
	require.NoError(t, copiar.Run(), "could not place the fixture in the container")

	output, err := exec.Command("docker", "exec", "ngx-bench",
		"nginx", "-t", "-c", "/tmp/syntax_surface.conf").CombinedOutput()
	require.NoErrorf(t, err,
		"real nginx refused the fixture, so it stopped being a description of "+
			"valid configuration:\n%s", output)
	require.Contains(t, string(output), "syntax is ok")
}
