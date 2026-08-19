package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/s0beran0/ngx/internal/cli"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// The human form is a VIEW; the JSON is the contract. Every test here forces
// --human on a non-TTY writer, which is exactly what the flag is for, and none
// of them asserts anything about the JSON -- the tests that pin the envelope
// live beside the commands that produce it.

// Without a filter, inspect answers the size of the configuration in one line.
// The check that it is not JSON is the point: the fallback in the renderer is
// json.MarshalIndent, so a missing RenderHuman is invisible unless a test asks
// whether the first byte is a brace.
func TestInspectHumanSummaryIsOneLine(t *testing.T) {
	code, text := runRaw(t, "--human", "inspect", "-c", filterFixture(t))

	require.Equal(t, output.ExitOK, code)
	require.NotContains(t, text, "{", "the summary is text, not the indented JSON fallback")
	require.Equal(t, "3 files, 4 servers, 1 location, 0 upstreams", strings.TrimSpace(text))
}

// A filtered answer says it is a subset, in the terminal too. A human reading
// "1 file" beside a three-file configuration, with nothing saying a filter was
// applied, would conclude the filter found everything there is.
func TestInspectHumanFilteredShowsScopeAndOutline(t *testing.T) {
	code, text := runRaw(t, "--human", "inspect", "-c", filterFixture(t), "--file", "sites/portal.conf")

	require.Equal(t, output.ExitOK, code)
	require.Contains(t, text, "scope: file=sites/portal.conf")
	require.Contains(t, text, "1 of 3 files emitted")
	require.Contains(t, text, "config_hash omitted")
	// The outline prints the blocks, indented by nesting, with the line each
	// one opens on -- which is what a person opens next.
	require.Contains(t, text, "server")
	require.Contains(t, text, "location /")
	// And it does NOT print every simple directive: that is the file itself,
	// which --format nginx already emits.
	require.NotContains(t, text, "proxy_pass")
}

// --full-tree is the deliberate exception: whoever asks for megabytes of tree
// asked for the tree, and summarizing it would answer a question they did not
// ask.
func TestInspectHumanFullTreeStaysIndentedJSON(t *testing.T) {
	code, text := runRaw(t, "--human", "inspect", "-c", filterFixture(t), "--full-tree")

	require.Equal(t, output.ExitOK, code)
	require.True(t, strings.HasPrefix(strings.TrimSpace(text), "{"),
		"--full-tree keeps the indented JSON")
	require.Contains(t, text, "\"directive\": \"server\"")
}

// get lists the matches with the file and the line, because that is what a
// person acts on next.
func TestGetHumanListsMatchesWithLocation(t *testing.T) {
	code, text := runRaw(t, "--human", "get", "-c", filterFixture(t), "--directive", "listen")

	require.Equal(t, output.ExitOK, code)
	require.Contains(t, text, "4 matches in 3 files")
	require.Contains(t, text, "nginx.conf:8")
	require.Contains(t, text, "listen 80")
	require.NotContains(t, text, "\"args\"", "the match list is text, not the JSON fallback")
}

// A match that opens a block does not unfold it -- it would bury the list the
// line is part of -- but the omission is visible instead of the block looking
// empty.
func TestGetHumanSaysHowMuchIsInsideABlock(t *testing.T) {
	code, text := runRaw(t, "--human", "get", "-c", filterFixture(t), "--directive", "location")

	require.Equal(t, output.ExitOK, code)
	require.Contains(t, text, "location / { 1 directive }")
}

// An empty answer is an answer: exit 0, one line saying so, and the diagnostic
// the renderer already wrote saying what WAS in scope.
func TestGetHumanEmptyAnswerSaysSoAndStaysOK(t *testing.T) {
	code, text := runRaw(t, "--human", "get", "-c", filterFixture(t), "--directive", "listem")

	require.Equal(t, output.ExitOK, code)
	require.Contains(t, text, "0 matches in 0 files")
	require.Contains(t, text, "matches no directive")
}

// The human path goes through the same redacted copy the JSON does. If it did
// not, --human would be a way around the redactor, and the terminal is exactly
// where somebody pastes the output into a ticket.
func TestGetHumanRendersTheRedactedValue(t *testing.T) {
	code, text := runRaw(t, "--human", "get", "-c", filterFixture(t), "--directive", "ssl_certificate_key")

	require.Equal(t, output.ExitOK, code)
	require.NotContains(t, text, "/etc/ssl/private/portal.key")
	require.Contains(t, text, "ssl_certificate_key ***")
}

// The project's omission rule, applied to text: a field that was not
// determined produces no sentence. "stopped" for a state nobody measured is
// the one failure that makes the human view worse than the JSON it replaces.
func TestStatusHumanDoesNotInventTheProcessState(t *testing.T) {
	var buf strings.Builder
	data := cli.StatusData{Process: cli.ProcessData{}}

	require.NoError(t, data.RenderHuman(&buf))
	require.Equal(t, "process state unavailable\n", buf.String())
	require.NotContains(t, buf.String(), "stopped")
}

// The version payload is a named map and not a struct precisely so the JSON
// does not move when RenderHuman is hung off it: same keys, same alphabetical
// order, same omission of a key that was never set.
func TestVersionHumanReadsAsTextAndTheJSONDoesNotMove(t *testing.T) {
	_, human := runRaw(t, "--human", "version")
	require.Contains(t, human, "ngx "+output.Version)
	require.Contains(t, human, "install channel: ")
	require.NotContains(t, human, "{")

	_, raw := runRaw(t, "version")
	var env struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &env))
	require.Equal(t, output.Version, env.Data["version"])
	require.NotEmpty(t, env.Data["install_channel"])
	require.True(t, strings.Contains(raw, `"data":{"install_channel"`),
		"install_channel still comes first, as a map serializes its keys sorted")
}

// The agent test of the release gate, as a test: the three questions the plan
// names have to be answerable from `ngx --help` alone. Each assertion is a
// piece of the answer that is IN the help, so a rewrite that drops one of them
// fails the build instead of being discovered by an agent that goes looking
// for the specification.
func TestRootHelpAnswersTheThreeQuestions(t *testing.T) {
	raw := runHelp(t, "--help")

	// "is the configuration valid?"
	require.Contains(t, raw, "ngx test")
	require.Contains(t, raw, "3 the nginx configuration is\ninvalid")

	// "which ports are listened on?"
	require.Contains(t, raw, "--directive listen --format table")

	// "how is one site configured?"
	require.Contains(t, raw, "--file example.com --format nginx")

	// And what every one of them needs first: where the configuration is,
	// and what the envelope looks like once an answer comes back.
	require.Contains(t, raw, "data.nginx.main_config")
	require.Contains(t, raw, "schema_version")
	require.Contains(t, raw, "diagnostics")
	require.Contains(t, raw, "OMITTED, never estimated")
}

// Every command carries examples, and they show INTENT: a "#" line above each
// one. An example that only shows which flags exist repeats the flag list two
// screens below it.
func TestEveryCommandHelpCarriesExamplesWithIntent(t *testing.T) {
	for _, command := range []string{"inspect", "get", "test", "status", "version", "update"} {
		t.Run(command, func(t *testing.T) {
			raw := runHelp(t, command, "--help")
			require.Contains(t, raw, "Examples:", "%s has no examples", command)
			examples := raw[strings.Index(raw, "Examples:"):]
			require.Contains(t, examples, "  # ",
				"%s's examples show syntax without saying what for", command)
			require.Contains(t, examples, "ngx "+command)
		})
	}
}

// The examples of the commands that read a file carry -c. An example copied as
// it stands has to work, and one that fails with "provide the configuration
// with -c" teaches the wrong lesson on the first call.
func TestReadingCommandExamplesCarryTheConfigFlag(t *testing.T) {
	for _, command := range []string{"inspect", "get"} {
		t.Run(command, func(t *testing.T) {
			raw := runHelp(t, command, "--help")
			examples := raw[strings.Index(raw, "Examples:"):strings.Index(raw, "Flags:")]
			for _, line := range strings.Split(examples, "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "ngx ") {
					continue
				}
				require.Contains(t, line, "-c /etc/nginx/nginx.conf",
					"an example that cannot be copied as it stands: %q", line)
			}
		})
	}
}
