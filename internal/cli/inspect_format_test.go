package cli_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s0beran0/ngx/internal/cli"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

func nginxFixture(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "nginx_format.conf")
}

// runRaw does not parse the output as an envelope: --format nginx and
// --format table do not produce one.
func runRaw(t *testing.T, args ...string) (output.ExitCode, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := cli.Execute(args, &out, &errBuf, false)
	return code, out.String()
}

// The test the whole of R6 exists for. Emitting the source text is a path
// that goes around the tree, so it also goes around the redactor that acts on
// the tree; if the substitution over the argument spans were missing, or were
// applied to the wrong range, the private key would come out whole.
func TestFormatNginxRedactsTheSourceText(t *testing.T) {
	code, text := runRaw(t, "inspect", "--full-tree", "-c", nginxFixture(t), "--format", "nginx")

	require.Equal(t, output.ExitOK, code)
	require.NotContains(t, text, "/etc/ssl/private/api.example.com.key",
		"the private key path cannot reach the output")
	require.NotContains(t, text, "s3cr3t-token", "the token cannot reach the output")
	require.Contains(t, text, "ssl_certificate_key ***;")
	require.Contains(t, text, "proxy_set_header Authorization ***;",
		"the header name stays readable: it says WHICH value was censored")
}

// The same file holds an "if", whose ArgSpans are nil because crossplane
// rewrites its Args -- see config.Node.ArgSpans. Nil is UNAVAILABLE, not "no
// arguments", and the proof that absence did not turn into a cut at the wrong
// place is that the directive comes out byte for byte as it was written.
func TestFormatNginxLeavesADirectiveWithoutArgSpansIntact(t *testing.T) {
	_, text := runRaw(t, "inspect", "--full-tree", "-c", nginxFixture(t), "--format", "nginx")

	require.Contains(t, text, "if ($request_method = POST) {")
	require.Contains(t, text, "return 405;")
}

// The text is the answer to "how is site X configured?" precisely because it
// costs a fraction of the tree. If this ever inverts, the flag has no reason
// to exist.
func TestFormatNginxIsSmallerThanTheJSONTree(t *testing.T) {
	_, text := runRaw(t, "inspect", "--full-tree", "-c", nginxFixture(t), "--format", "nginx")
	_, tree := runRaw(t, "inspect", "--full-tree", "-c", nginxFixture(t), "--json")

	require.Less(t, len(text)*4, len(tree),
		"the text has to be several times smaller than the tree: %d vs %d", len(text), len(tree))
}

// Comments are nodes, so they survive; what the caller asked NOT to see does
// not come back just because it sits in the same file.
func TestFormatNginxKeepsCommentsAndOnlyTheFilteredNodes(t *testing.T) {
	_, full := runRaw(t, "inspect", "--full-tree", "-c", nginxFixture(t), "--format", "nginx")
	require.Contains(t, full, "# the site this fixture stands for")

	_, filtered := runRaw(t, "inspect", "--server", "api.example.com", "-c", nginxFixture(t), "--format", "nginx")
	require.Contains(t, filtered, "server_name api.example.com;")
	require.NotContains(t, filtered, "events {}",
		"the filter decides what is emitted; the rest of the file does not come back")
}

// The defect this format is one flag away from: --server keeps the ancestors
// of the matched block ("http") with their Block narrowed, but their Span
// still covers the WHOLE original block. Cutting that span would print the
// sibling servers -- and those siblings are not in the tree that was redacted,
// so their private keys would come out in the clear. The wrapper is rebuilt
// around what the filter kept.
func TestFormatNginxDoesNotLeakTheSiblingsAFilterRemoved(t *testing.T) {
	code, text := runRaw(t, "inspect", "--server", "api.example.com",
		"-c", filepath.Join("testdata", "two_sites.conf"), "--format", "nginx")

	require.Equal(t, output.ExitOK, code)
	require.Contains(t, text, "server_name api.example.com;")
	require.Contains(t, text, "ssl_certificate_key ***;")
	require.NotContains(t, text, "admin.example.com",
		"a server the filter removed cannot come back through the ancestor's span")
	require.NotContains(t, text, "/etc/ssl/private/admin.example.com.key",
		"the removed server was never redacted: its key must not be printed")
	require.Contains(t, text, "http {",
		"the block that holds the answer is still shown, rebuilt around what was kept")
	require.Contains(t, strings.TrimRight(text, "\n"), "}")
}

// Without a tree there is no text to emit, and an empty stdout would be read
// as "this site has no configuration".
func TestFormatNginxWithoutATreeRefuses(t *testing.T) {
	code, raw := runRaw(t, "inspect", "-c", nginxFixture(t), "--format", "nginx")

	require.Equal(t, output.ExitUsage, code)
	require.Contains(t, raw, "--full-tree")
	require.Contains(t, raw, "NGX-0002")
}

// A combined tree keeps no Source: its nodes come from several files and each
// span only means anything against the file it was cut from. Slicing them
// against another file would print another file's bytes as if they were this
// one's.
func TestFormatNginxWithCombineRefuses(t *testing.T) {
	code, raw := runRaw(t, "inspect", "--full-tree", "--combine", "-c", nginxFixture(t), "--format", "nginx")

	require.Equal(t, output.ExitUsage, code)
	require.Contains(t, raw, "--combine")
}

// The refusal has to survive the format that caused it: rendering the error
// envelope as nginx text would answer "this is not nginx text" and the real
// reason would never be printed.
func TestFormatNginxOnDataThatIsNotConfigurationRefusesWithTheEnvelope(t *testing.T) {
	code, raw := runRaw(t, "version", "--format", "nginx")

	require.Equal(t, output.ExitUsage, code)
	require.Contains(t, raw, `"ok":false`)
	require.Contains(t, raw, "not nginx configuration text")
}

// R7: the summary is flat, so it has a table.
func TestFormatTableEmitsTheSummaryAsTSV(t *testing.T) {
	code, raw := runRaw(t, "inspect", "-c", nginxFixture(t), "--format", "table")

	require.Equal(t, output.ExitOK, code)
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	require.Len(t, lines, 2)
	require.Equal(t, "files\tservers\tlocations\tupstreams", lines[0])
	require.Equal(t, "1\t1\t1\t0", lines[1])
}

// The other half of R7: nested data REFUSES, and says which format to use. A
// flattened tree is a wrong answer that looks like an answer.
func TestFormatTableOnTheTreeRefuses(t *testing.T) {
	code, raw := runRaw(t, "inspect", "--full-tree", "-c", nginxFixture(t), "--format", "table")

	require.Equal(t, output.ExitUsage, code)
	require.Contains(t, raw, "nested")
	require.Contains(t, raw, "--json")
}

// --format is the long spelling of --json/--human. Two spellings of the same
// choice on one command line have no coherent winner.
func TestFormatConflictsWithTheShorthandFlags(t *testing.T) {
	for _, flag := range []string{"--json", "--human"} {
		code, raw := runRaw(t, "version", "--format", "json", flag)
		require.Equal(t, output.ExitUsage, code, flag)
		require.Contains(t, raw, "mutually exclusive", flag)
	}
}

// --field and --query take a projection OF the envelope; --format chooses how
// the envelope is presented. Asking for both at once has no answer.
func TestFormatConflictsWithTheProjectionFlags(t *testing.T) {
	code, raw := runRaw(t, "version", "--format", "json", "--field", "data.version")

	require.Equal(t, output.ExitUsage, code)
	require.Contains(t, raw, "--field and --format are mutually exclusive")
}

// A format that does not exist is refused by name, with the list of the ones
// that do. Falling back to JSON would answer a question that was not asked.
func TestUnknownFormatIsRefusedWithTheValidValues(t *testing.T) {
	code, raw := runRaw(t, "version", "--format", "yaml")

	require.Equal(t, output.ExitUsage, code)
	require.Contains(t, raw, "yaml")
	require.Contains(t, raw, "nginx")
	require.Contains(t, raw, "table")
}
