package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/output"
)

// The cost of an answer, as a checked property.
//
// This project's whole premise is that a program pays for output by the token,
// so a command that quietly triples what it emits is a regression even when
// every other test passes. Nothing here was catching that: the formats were
// tested for correctness and never for size.
//
// The unit is BYTES, not tokens, and the reason is worth stating rather than
// hiding. A real tokenizer is not available to a Go test, and vendoring one to
// approximate a model nobody here runs would be precision theatre. Bytes are
// exactly measurable, they move with tokens for output of this shape, and what
// matters is the RATIO between two forms of the same answer -- which bytes
// preserve.
//
// The absolute numbers came from tiktoken (o200k_base) against a 61-file
// configuration, and are recorded in the plan for v0.2.1. What is asserted here
// is the ratio, because that is the part a code change can break.
//
// The budgets below hold about 35% headroom over what the fixture measures
// today. Tight enough that a doubling trips them, loose enough that reordering
// a field does not.

// atRealisticDepth replaces the fixture's temporary directory with a path of
// the length these commands actually run against.
//
// This is not cosmetic, and leaving it out produced a wrong number. A file path
// under t.TempDir() is around 85 characters -- /var/folders/9m/59p3.../001 --
// against 18 for /etc/nginx/conf.d. Both `file` and `ref` survive into the lean
// form, deliberately, so the harness's long path inflates the CHEAP side of the
// comparison and makes the saving look smaller than it is: measured 0.72 with
// the temporary path against 0.58 with a realistic one, for the same code.
//
// Normalising is the honest correction, because the thing being normalised is
// an artefact of where the test writes its files and nothing about the product.
func atRealisticDepth(out, dir string) string {
	return strings.ReplaceAll(out, dir, "/etc/nginx")
}

// ceilings are the byte budgets, generous enough that only a real regression
// trips them and tight enough that a doubling does.
//
// Each one names the question it answers, because a budget with no question
// attached is a number somebody will raise rather than investigate.
var ceilings = []struct {
	question string
	args     []string
	maxBytes int
	why      string
}{
	{
		question: "which ports are listened on, as a table",
		args:     []string{"get", "--directive", "listen", "--format", "table"},
		maxBytes: 2800,
		why: "the cheapest full answer to the commonest question. 60 rows of TSV " +
			"with the shared path extracted once",
	},
	{
		question: "which ports are listened on, one value each",
		args:     []string{"get", "--directive", "listen", "--query", "[.data.matches[].args[0]]"},
		maxBytes: 420,
		why:      "a query returns the values and nothing else; this is the floor",
	},
	{
		question: "how is one site configured, as nginx text",
		args:     []string{"inspect", "--file", "site-7.conf", "--format", "nginx"},
		maxBytes: 450,
		why: "the source of that file is around 260 bytes, and the answer has to " +
			"stay within a small factor of what it describes",
	},
	{
		question: "how is one site configured, as the tree",
		args:     []string{"inspect", "--file", "site-7.conf", "--json", "--detail", "answer"},
		maxBytes: 1800,
		why:      "detail=answer drops the byte spans a reader never uses",
	},
	{
		question: "what is in this configuration at all",
		args:     []string{"inspect", "--json"},
		maxBytes: 420,
		why: "the summary is the whole point of inspect not returning the tree by " +
			"default: a few hundred bytes instead of a hundred thousand",
	},
}

func TestTheCostOfAnAnswerStaysWithinBudget(t *testing.T) {
	root := buildFixture(t)

	for _, c := range ceilings {
		t.Run(c.question, func(t *testing.T) {
			args := append([]string{"-c", root}, c.args...)
			code, raw := runRaw(t, args...)
			require.Equalf(t, output.ExitOK, code, "the command failed:\n%s", raw)
			out := atRealisticDepth(raw, filepath.Dir(root))

			require.LessOrEqualf(t, len(out), c.maxBytes,
				"this answer costs %d bytes and the budget is %d.\n\n"+
					"The budget is not a style rule: %s.\n\n"+
					"If the growth is deliberate, raise the number AND say here what "+
					"the caller gets for it. If it is not, the output grew by accident, "+
					"which is what this test exists to notice.",
				len(out), c.maxBytes, c.why)
		})
	}
}

// The ratio that justifies detail=answer, asserted rather than claimed.
//
// If a future change makes the lean form stop being meaningfully cheaper, the
// flag is dead weight and its help text is a lie -- and nothing else would
// report that.
func TestDetailAnswerIsSubstantiallyCheaperThanFull(t *testing.T) {
	root := buildFixture(t)

	for _, c := range []struct {
		what string
		args []string
	}{
		{"get over 60 matches", []string{"get", "--directive", "listen", "--json"}},
		{"the whole tree", []string{"inspect", "--full-tree", "--json"}},
		{"one file", []string{"inspect", "--file", "site-7.conf", "--json"}},
	} {
		t.Run(c.what, func(t *testing.T) {
			code, fullRaw := runRaw(t, append([]string{"-c", root}, c.args...)...)
			require.Equal(t, output.ExitOK, code, fullRaw)
			code, leanRaw := runRaw(t, append(append([]string{"-c", root}, c.args...),
				"--detail", "answer")...)
			require.Equal(t, output.ExitOK, code, leanRaw)

			dir := filepath.Dir(root)
			full, lean := atRealisticDepth(fullRaw, dir), atRealisticDepth(leanRaw, dir)

			ratio := float64(len(lean)) / float64(len(full))
			require.Lessf(t, ratio, 0.75,
				"detail=answer is only %.0f%% of full, and the flag promises around a "+
					"third off. Either the lean shape grew a field it does not need, or "+
					"the full shape lost one -- both are worth knowing",
				100*ratio)

			// And it still carries what makes it actionable: a reader has to be
			// able to name the node afterwards.
			require.Contains(t, lean, `"ref"`,
				"the lean form dropped ref, so a reader cannot act on what it found")
			require.NotContains(t, lean, `"span"`,
				"the lean form still carries byte spans, which is what it exists to omit")
		})
	}
}

// The table extracts the shared path once. Measured at 20% cheaper on this
// shape; grouping the rows by file instead measured 30% MORE, and that finding
// is why the code does what it does.
func TestTheTableExtractsTheSharedPathOnce(t *testing.T) {
	root := buildFixture(t)

	code, out := runRaw(t, "-c", root, "get", "--directive", "proxy_pass", "--format", "table")
	require.Equalf(t, output.ExitOK, code, "%s", out)

	dir := filepath.Dir(root)
	require.Contains(t, out, "relative to "+filepath.Join(dir, "conf.d"),
		"the shared prefix was not extracted")

	rows := 0
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "ref\t") {
			continue
		}
		rows++
		require.NotContainsf(t, line, dir,
			"a row still carries the shared prefix: %s", line)
	}
	require.Greater(t, rows, 10, "the fixture did not produce enough rows to measure")
}

// --- helpers ---------------------------------------------------------------

// buildFixture writes a configuration of the shape the budgets were measured
// against: one file per site, which is what every distribution's conf.d looks
// like and the only shape where per-node path repetition costs anything.
func buildFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755))
	root := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(root, []byte(
		"events { worker_connections 1024; }\nhttp {\n  include conf.d/*.conf;\n}\n"), 0o644))

	for i := 1; i <= 60; i++ {
		body := fmt.Sprintf(
			"server {\n    listen 80;\n    server_name site-%03d.example.com;\n"+
				"    location / {\n        proxy_pass http://backend-%03d;\n    }\n}\n", i, i)
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "conf.d", fmt.Sprintf("site-%d.conf", i)), []byte(body), 0o644))
	}
	return root
}
