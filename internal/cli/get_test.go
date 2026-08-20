package cli_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s0beran0/ngx/internal/cli"
	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// diagnosticWithCode returns the diagnostic carrying the code, or fails.
func diagnosticWithCode(t *testing.T, env *output.Envelope, code string) output.Diagnostic {
	t.Helper()
	for _, d := range env.Diagnostics {
		if d.Code == code {
			return d
		}
	}
	require.FailNowf(t, "diagnostic not found", "no diagnostic with code %s in %+v", code, env.Diagnostics)
	return output.Diagnostic{}
}

func TestGetReturnsEveryOccurrenceOfTheDirective(t *testing.T) {
	code, env, raw := runInspect(t, "get", "--directive", "listen", "-c", filterFixture(t))

	require.Equal(t, output.ExitOK, code)
	require.True(t, env.OK)
	require.Equal(t, "get", env.Command)

	var response struct {
		Data struct {
			Matches []struct {
				Directive string   `json:"directive"`
				Args      []string `json:"args"`
			} `json:"matches"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))
	require.Len(t, response.Data.Matches, 4)
	for _, m := range response.Data.Matches {
		require.Equal(t, "listen", m.Directive)
	}
}

// --directive is required because get has no default question: without it the
// command would have to mean "everything", which inspect --full-tree already
// is, under a name that promises the opposite.
func TestGetRequiresTheDirectiveFlag(t *testing.T) {
	code, env, _ := runInspect(t, "get", "-c", fixture(t))

	require.Equal(t, output.ExitUsage, code)
	require.False(t, env.OK)
	require.Contains(t, env.Diagnostics[0].Message, "directive")
}

// An empty answer is an ANSWER: exit 0, ok true, and a list that is empty
// rather than null. An agent that treated "there is no listen in this file"
// as a failure would retry a question that has been answered.
func TestGetWithNoMatchSucceedsWithAnEmptyList(t *testing.T) {
	code, env, raw := runInspect(t, "get", "--directive", "lsiten", "-c", fixture(t))

	require.Equal(t, output.ExitOK, code)
	require.True(t, env.OK)
	require.Contains(t, raw, `"matches":[]`)
	require.NotContains(t, raw, `"matches":null`)
}

// And it says what WAS there: an empty result and a misspelt name are the
// same output otherwise, and the caller has no way to tell which one it got.
func TestGetNoMatchListsTheDirectivesInScope(t *testing.T) {
	_, env, _ := runInspect(t, "get", "--directive", "lsiten", "-c", fixture(t))

	d := diagnosticWithCode(t, env, "NGX-0106")
	require.Equal(t, output.SeverityInfo, d.Severity)
	require.Contains(t, d.Message, "listen")
	require.Contains(t, d.Message, "server_name")
}

// The three no-match diagnostics differ because the three questions differ:
// which of the filters emptied the result is exactly what the caller has to
// change, and one generic message would leave them changing the wrong flag.
func TestGetInWithNoMatchNamesTheBlocksThatDoEncloseIt(t *testing.T) {
	code, env, _ := runInspect(t, "get", "--directive", "listen", "--in", "upstream", "-c", fixture(t))

	require.Equal(t, output.ExitOK, code)
	d := diagnosticWithCode(t, env, "NGX-0107")
	require.Equal(t, output.SeverityInfo, d.Severity)
	require.Contains(t, d.Message, "server")
	require.Contains(t, d.Message, "http")
}

func TestGetValueWithNoMatchListsTheValuesFound(t *testing.T) {
	code, env, _ := runInspect(t, "get",
		"--directive", "server_name", "--value", "nope.example.com", "-c", fixture(t))

	require.Equal(t, output.ExitOK, code)
	d := diagnosticWithCode(t, env, "NGX-0108")
	require.Equal(t, output.SeverityInfo, d.Severity)
	require.Contains(t, d.Message, "api.example.com")
}

func TestGetValueMatchesOneArgumentExactly(t *testing.T) {
	_, _, raw := runInspect(t, "get",
		"--directive", "server_name", "--value", "api.example.com", "-c", fixture(t))
	require.Contains(t, raw, "api.example.com")

	// A substring rule would make --value 44 hit "listen 443", and every
	// answer this command gives would become a guess.
	_, env, _ := runInspect(t, "get",
		"--directive", "server_name", "--value", "example.com", "-c", fixture(t))
	diagnosticWithCode(t, env, "NGX-0108")
}

// --in reaches an ancestor at any depth, not only the immediate parent: a
// listen inside a location inside a server IS inside a server, and answering
// "no match" for it would be technically defensible and practically a lie.
func TestGetInMatchesAnAncestorAtAnyDepth(t *testing.T) {
	_, _, raw := runInspect(t, "get", "--directive", "proxy_pass", "--in", "server", "-c", fixture(t))

	var response struct {
		Data struct {
			Matches []json.RawMessage `json:"matches"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))
	require.Len(t, response.Data.Matches, 1)
}

// The case that made --in worth implementing across files: on the layout
// every distribution ships, the "http" that encloses a server block is
// written in nginx.conf and the block lives in a file it includes. Reading
// the nesting one file at a time would answer "no match" on the most ordinary
// nginx there is.
func TestGetInCrossesIncludes(t *testing.T) {
	_, _, raw := runInspect(t, "get", "--directive", "server_name", "--in", "http", "-c", filterFixture(t))

	var response struct {
		Data struct {
			Matches []struct {
				File string `json:"file"`
			} `json:"matches"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))
	require.Len(t, response.Data.Matches, 4)

	files := map[string]bool{}
	for _, m := range response.Data.Matches {
		files[filepath.Base(filepath.Dir(m.File))] = true
	}
	require.True(t, files["sites"], "the included file did not come out: %s", raw)
	require.True(t, files["sites-extra"], "the second included file did not come out: %s", raw)
}

// Every result of get is a subset by definition, so the hash of the tree it
// was cut from must not travel beside it: a hash published next to a subset
// is a valid hash OF A SUBSET, and v0.2 will use it for optimistic locking.
func TestGetOmitsTheConfigHashAndMarksTheScope(t *testing.T) {
	_, env, raw := runInspect(t, "get", "--directive", "listen", "--in", "server", "-c", fixture(t))

	require.Empty(t, env.Meta.ConfigHash)
	// The quoted key with its colon, so that scope.config_hash_omitted --
	// which is the announcement that the field is gone -- does not satisfy
	// the assertion that the field is gone.
	require.NotContains(t, raw, `"config_hash":`)

	var response struct {
		Data struct {
			Scope struct {
				Partial bool `json:"partial"`
				Filters struct {
					Directive string `json:"directive"`
					In        string `json:"in"`
				} `json:"filters"`
				FilesEmitted      int  `json:"files_emitted"`
				ConfigHashOmitted bool `json:"config_hash_omitted"`
			} `json:"scope"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))
	require.True(t, response.Data.Scope.Partial)
	require.True(t, response.Data.Scope.ConfigHashOmitted)
	require.Equal(t, "listen", response.Data.Scope.Filters.Directive)
	require.Equal(t, "server", response.Data.Scope.Filters.In)
	require.Equal(t, 1, response.Data.Scope.FilesEmitted)

	diagnosticWithCode(t, env, "NGX-0105")
}

// The summary describes the WHOLE configuration that was read, filtered or
// not: it is what makes two answers comparable, and what tells a narrow
// result from a small configuration.
func TestGetSummarizesTheWholeConfiguration(t *testing.T) {
	_, _, raw := runInspect(t, "get", "--directive", "listen", "-c", fixture(t))

	var response struct {
		Data struct {
			Summary struct {
				Servers   int `json:"servers"`
				Locations int `json:"locations"`
			} `json:"summary"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))
	require.Equal(t, 1, response.Data.Summary.Servers)
	require.Equal(t, 2, response.Data.Summary.Locations)
}

// A comment is a node with Directive "#", and get answers about directives.
// Returning comments would make --directive '#' a way of asking for something
// that is not a directive at all.
func TestGetNeverReturnsComments(t *testing.T) {
	code, env, raw := runInspect(t, "get", "--directive", "#", "-c",
		filepath.Join("testdata", "two_sites.conf"))

	require.Equal(t, output.ExitOK, code)
	require.Contains(t, raw, `"matches":[]`)
	diagnosticWithCode(t, env, "NGX-0106")
}

// --file and --server keep the meaning they have on inspect, which is what
// makes get free of a vocabulary of its own.
func TestGetNarrowsWithTheInspectFilters(t *testing.T) {
	_, _, raw := runInspect(t, "get", "--directive", "listen", "--file", "sites-extra", "-c", filterFixture(t))

	var response struct {
		Data struct {
			Matches []struct {
				Args []string `json:"args"`
			} `json:"matches"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))
	require.Len(t, response.Data.Matches, 1)
	require.Equal(t, []string{"8080"}, response.Data.Matches[0].Args)
}

// A --file that names nothing is still a usage error, as it is on inspect:
// there the caller named a PLACE that does not exist, which is a different
// failure from asking about a directive that is not there.
func TestGetWithAFileThatMatchesNothingIsAUsageError(t *testing.T) {
	code, env, _ := runInspect(t, "get", "--directive", "listen", "--file", "absent.conf", "-c", filterFixture(t))

	require.Equal(t, output.ExitUsage, code)
	require.False(t, env.OK)
	require.Equal(t, "NGX-0102", env.Diagnostics[0].Code)
}

func TestGetRedactsTheValuesLikeInspect(t *testing.T) {
	_, _, raw := runInspect(t, "get", "--directive", "ssl_certificate_key", "-c", fixture(t))

	require.NotContains(t, raw, "/etc/ssl/private/api.key")
	require.Contains(t, raw, output.RedactedValue)
	// The indices are what tell a censored value from a configuration that
	// literally holds three asterisks.
	require.Contains(t, raw, `"redacted_args":[0]`)
}

func TestGetTableEmitsOneRowPerMatch(t *testing.T) {
	code, raw := runRaw(t, "get", "--directive", "listen", "--format", "table", "-c", filterFixture(t))

	require.Equal(t, output.ExitOK, code)
	lines := nonCommentLines(raw)
	require.Equal(t, "ref\tline\tdirective\targs", lines[0])
	require.Len(t, lines, 5, "header plus four listens: %s", raw)
	require.Contains(t, lines[3], "\tlisten\t443 ssl")
}

// The first column has to be addressable, which is the whole reason it stopped
// being "id". Every row names a different node, and the ref resolves to the
// node the row describes -- not to the first of several sharing an ID.
func TestGetTableRefsAreDistinctAndResolve(t *testing.T) {
	path := filterFixture(t)
	_, raw := runRaw(t, "get", "--directive", "listen", "--format", "table", "-c", path)

	tree, err := config.Parse(config.ParseOptions{Path: path})
	require.NoError(t, err)

	rows := nonCommentLines(raw)[1:]
	seen := map[string]bool{}
	for _, row := range rows {
		ref := strings.Split(row, "\t")[0]
		require.Falsef(t, seen[ref], "two rows carry the same ref: %s", ref)
		seen[ref] = true

		node := config.FindByRef(tree, ref)
		require.NotNilf(t, node, "the ref in the table resolves to nothing: %s", ref)
		require.Equal(t, "listen", node.Directive)
	}
	require.Len(t, seen, len(rows))
}

// The table refuses what it cannot hold, instead of flattening it: a row that
// dropped the contents of a server block would be a wrong answer that looks
// like an answer.
func TestGetTableRefusesAMatchWithABlock(t *testing.T) {
	code, env, _ := runInspect(t, "get", "--directive", "server", "--format", "table", "-c", fixture(t))

	require.Equal(t, output.ExitUsage, code)
	require.Contains(t, env.Diagnostics[0].Message, "opens a block")
}

// Same rule one level down: the args column joins the arguments with a space,
// so an argument that holds one would silently become two columns' worth of
// meaning. Refusing is the loud half of the choice H4 records; the escaping
// rule of the TSV writer is the other half.
func TestGetTableRefusesAnArgumentWithASpace(t *testing.T) {
	code, env, _ := runInspect(t, "get", "--directive", "add_header", "--format", "table",
		"-c", filepath.Join("testdata", "spaced_args.conf"))

	require.Equal(t, output.ExitUsage, code)
	require.Contains(t, env.Diagnostics[0].Message, "holds a space")
}

// --format nginx cuts the bytes of the matched node out of the file it came
// from, which is why the output is the author's own text.
func TestGetNginxEmitsTheSourceText(t *testing.T) {
	code, raw := runRaw(t, "get", "--directive", "location", "--format", "nginx", "-c", fixture(t))

	require.Equal(t, output.ExitOK, code)
	require.Contains(t, raw, "location / {")
	require.Contains(t, raw, "proxy_pass http://backend;")
	require.Contains(t, raw, "location /health {")
	// What was not asked for stays out: the server around the locations.
	require.NotContains(t, raw, "server_name api.example.com;")
}

// The source text is not a way around the redactor: it is emitted through the
// substitutions the redacted copy marks, so a secret comes out as *** here
// too.
func TestGetNginxRedactsTheSourceText(t *testing.T) {
	_, raw := runRaw(t, "get", "--directive", "server", "--format", "nginx", "-c", fixture(t))

	require.NotContains(t, raw, "/etc/ssl/private/api.key")
	require.Contains(t, raw, "ssl_certificate_key "+output.RedactedValue)
}

func TestGetAnswersFieldAndQuery(t *testing.T) {
	_, raw := runRaw(t, "get", "--directive", "listen", "--field", "data.matches.0.args.0", "-c", fixture(t))
	require.Equal(t, "443\n", raw)

	_, raw = runRaw(t, "get", "--directive", "server_name",
		"--query", ".data.matches[].args[]", "-c", filterFixture(t))
	require.Equal(t, []string{
		"legacy.example.com",
		"portal.example.com",
		"portal.example.com",
		"portal-admin.example.com",
	}, strings.Fields(strings.TrimSpace(raw)))
}

// The help is the documentation an agent reads before its first call, so the
// examples have to be in it -- and it must not promise a saving in READING
// that only --file could ever deliver and does not deliver yet.
func TestGetHelpShowsExamplesAndPromisesNoPruning(t *testing.T) {
	raw := runHelp(t, "get", "--help")
	// The examples carry -c because the command has no default configuration
	// path: an example copied as it stands has to work, and one that fails
	// with "provide the configuration with -c" teaches the wrong lesson.
	require.Contains(t, raw, "ngx get -c /etc/nginx/nginx.conf --directive listen")
	require.Contains(t, raw, "--value api.example.com")
	// The intent, not the syntax: an example that only shows which flags
	// exist repeats the flag list two screens below it.
	require.Contains(t, raw, "# which ports are listened on?")
	require.NotContains(t, raw, "reads only")
}

// runHelp captures what cobra writes for --help, which goes to the command's
// error writer and not to stdout: the answer stream stays clean for whoever
// is piping it.
func runHelp(t *testing.T, args ...string) string {
	t.Helper()
	var out, errBuf bytes.Buffer
	cli.Execute(args, &out, &errBuf, false)
	return errBuf.String()
}

// nonCommentLines drops the "#" lines the diagnostics come out as, leaving
// the TSV itself.
func nonCommentLines(raw string) []string {
	out := make([]string, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}
