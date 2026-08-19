package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// runWithRedactRules runs the CLI with an isolated settings file that declares
// exactly the given redaction rules. It is the only way to point redaction at
// a directive the defaults do not cover -- "if", the one directive whose
// ArgSpans are unavailable.
func runWithRedactRules(t *testing.T, rules string, args ...string) (output.ExitCode, string) {
	t.Helper()
	global, local := isolatedPaths(t)
	require.NoError(t, os.WriteFile(local, []byte(rules), 0o644))

	var out, errBuf bytes.Buffer
	ctx := &Context{
		Flags:              &GlobalFlags{},
		Renderer:           &output.Renderer{Out: &out, IsTTY: false},
		GlobalSettingsPath: global,
		LocalSettingsPath:  local,
		Getenv:             os.Getenv,
	}
	code := execute(NewRoot(ctx), ctx, args, &errBuf)
	return code, out.String()
}

// The second half of the ArgSpans rule. Nil means UNAVAILABLE, and when a
// redaction rule DOES match such a directive there is no span to substitute:
// the answer is neither to guess a range nor to emit the value, but to fall
// back to the one range that is still true -- HeadSpan, the name plus every
// argument -- and replace it whole.
//
// Over-redacting the arguments of an "if" is the safe side of the trade-off;
// a guessed cut would print half of the value it was meant to hide.
func TestFormatNginxFallsBackToHeadSpanWhenArgSpansAreUnavailable(t *testing.T) {
	code, text := runWithRedactRules(t,
		"output:\n  redact:\n    - if\n    - ssl_certificate_key\n",
		"inspect", "--full-tree", "-c", filepath.Join("testdata", "nginx_format.conf"),
		"--format", "nginx")

	require.Equal(t, output.ExitOK, code, "output: %s", text)
	require.NotContains(t, text, "$request_method",
		"the matched rule covers the whole head: no argument of the if survives")
	require.Contains(t, text, "if *** {",
		"the directive keeps its name and its block; only the head is replaced")
	require.Contains(t, text, "return 405;",
		"the block of the if is outside HeadSpan and is not touched")
	require.Contains(t, text, "ssl_certificate_key ***;",
		"the directive that does have spans is still redacted argument by argument")
}

// With redaction turned off through the settings file the source comes out as
// it is -- the same thing --json does -- and the warning that says so travels
// as an nginx comment, because a format with nowhere to put a diagnostic must
// not silently drop the one that announces secrets are going out in the open.
func TestFormatNginxCarriesTheDiagnosticsAsComments(t *testing.T) {
	code, text := runWithRedactRules(t,
		"output:\n  redact: []\n",
		"inspect", "--full-tree", "-c", filepath.Join("testdata", "nginx_format.conf"),
		"--format", "nginx")

	require.Equal(t, output.ExitOK, code)
	require.Contains(t, text, "# warning: redaction is OFF")
	require.Contains(t, text, "/etc/ssl/private/api.example.com.key",
		"with the rules turned off the text is the source, exactly as --json is the tree")
}
