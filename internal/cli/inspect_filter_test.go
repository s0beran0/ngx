package cli_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/stretchr/testify/require"
)

// filterFixture is a configuration with the two shapes the filters exist for:
// the same base name in two directories (sites/portal.conf and
// sites-extra/portal.conf), and one server_name served by two blocks.
func filterFixture(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "filters", "nginx.conf")
}

// filterNode is the reader's view of a node for these tests: read out of the
// JSON by the published names, so the test breaks if a tag moves.
type filterNode struct {
	Directive string       `json:"directive"`
	Args      []string     `json:"args"`
	File      string       `json:"file"`
	ID        string       `json:"id"`
	Block     []filterNode `json:"block"`
}

type filterResponse struct {
	Data struct {
		Config []struct {
			File   string       `json:"file"`
			Parsed []filterNode `json:"parsed"`
		} `json:"config"`
		Summary struct {
			Files   int `json:"files"`
			Servers int `json:"servers"`
		} `json:"summary"`
		Scope *struct {
			Partial bool `json:"partial"`
			Filters struct {
				File   string `json:"file"`
				Server string `json:"server"`
			} `json:"filters"`
			FilesEmitted      int  `json:"files_emitted"`
			ConfigHashOmitted bool `json:"config_hash_omitted"`
		} `json:"scope"`
	} `json:"data"`
}

func decodeFilter(t *testing.T, raw string) filterResponse {
	t.Helper()
	var r filterResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &r), "output: %s", raw)
	return r
}

func filesOf(r filterResponse) []string {
	out := make([]string, 0, len(r.Data.Config))
	for _, f := range r.Data.Config {
		out = append(out, f.File)
	}
	return out
}

// serversIn collects every server block of the response, at any depth.
func serversIn(r filterResponse) []filterNode {
	out := []filterNode{}
	var walk func([]filterNode)
	walk = func(nodes []filterNode) {
		for _, n := range nodes {
			if n.Directive == "server" && len(n.Block) > 0 {
				out = append(out, n)
			}
			walk(n.Block)
		}
	}
	for _, f := range r.Data.Config {
		walk(f.Parsed)
	}
	return out
}

// --- the default: summary, not the tree -------------------------------------

// The dump is 1.6 MB on a real production nginx. Whoever asks a question about
// one file must not pay for the whole configuration by default.
func TestInspectWithNoFilterReturnsOnlyTheSummary(t *testing.T) {
	code, _, raw := runInspect(t, "inspect", "-c", filterFixture(t))
	require.Equal(t, output.ExitOK, code)

	var decoded struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &decoded))

	require.Contains(t, decoded.Data, "summary")
	require.NotContains(t, decoded.Data, "config",
		"the key is absent, not an empty list: [] would claim the configuration has no files")
	require.NotContains(t, decoded.Data, "scope",
		"nothing was filtered, so there is no subset to mark")
}

func TestInspectFullTreeEmitsEveryFile(t *testing.T) {
	code, _, raw := runInspect(t, "inspect", "--full-tree", "-c", filterFixture(t))
	require.Equal(t, output.ExitOK, code)

	r := decodeFilter(t, raw)
	require.Len(t, r.Data.Config, 3)
	require.Nil(t, r.Data.Scope, "the whole tree is not a subset")
}

// --- --file -----------------------------------------------------------------

// The fragment matches anywhere in the PATH, which is what lets two files with
// the same base name be told apart at all.
func TestInspectFileMatchesAgainstTheWholePath(t *testing.T) {
	code, _, raw := runInspect(t, "inspect",
		"--file", filepath.Join("sites", "portal.conf"), "-c", filterFixture(t))
	require.Equal(t, output.ExitOK, code)

	r := decodeFilter(t, raw)
	require.Len(t, filesOf(r), 1)
	require.Contains(t, filesOf(r)[0], filepath.Join("sites", "portal.conf"))
}

func TestInspectFileWithAbsolutePathIsExact(t *testing.T) {
	abs, err := filepath.Abs(filepath.Join("testdata", "filters", "sites", "portal.conf"))
	require.NoError(t, err)
	top, err := filepath.Abs(filterFixture(t))
	require.NoError(t, err)

	code, _, raw := runInspect(t, "inspect", "--file", abs, "-c", top)
	require.Equal(t, output.ExitOK, code)
	require.Equal(t, []string{abs}, filesOf(decodeFilter(t, raw)))
}

// A value starting with "/" is a path, not a fragment: "/portal.conf" is a
// substring of two of the files read and still matches neither, because
// exactness is the whole point of writing the leading slash.
func TestInspectFileWithAbsolutePathDoesNotFallBackToSubstring(t *testing.T) {
	code, env, _ := runInspect(t, "inspect", "--file", "/portal.conf", "-c", filterFixture(t))

	require.Equal(t, output.ExitUsage, code)
	require.Equal(t, "NGX-0102", env.Diagnostics[0].Code)
}

// The case that motivated the filters: never pick one of several. An answer
// chosen by the tool teaches nobody to be precise, and is wrong half the time.
func TestInspectFileAmbiguityListsTheCandidatesAndRefuses(t *testing.T) {
	code, env, _ := runInspect(t, "inspect", "--file", "portal.conf", "-c", filterFixture(t))

	require.Equal(t, output.ExitUsage, code)
	require.False(t, env.OK)
	require.Len(t, env.Diagnostics, 1)
	require.Equal(t, "NGX-0101", env.Diagnostics[0].Code)
	require.Contains(t, env.Diagnostics[0].Message, filepath.Join("sites", "portal.conf"))
	require.Contains(t, env.Diagnostics[0].Message, filepath.Join("sites-extra", "portal.conf"))
}

// An empty result and a misspelt name look identical unless the refusal says
// what WAS there.
func TestInspectFileNoMatchSaysWhatWasAvailable(t *testing.T) {
	code, env, _ := runInspect(t, "inspect", "--file", "does-not-exist", "-c", filterFixture(t))

	require.Equal(t, output.ExitUsage, code)
	require.Equal(t, "NGX-0102", env.Diagnostics[0].Code)
	msg := env.Diagnostics[0].Message
	require.Contains(t, msg, "nginx.conf")
	require.Contains(t, msg, filepath.Join("sites", "portal.conf"))
	require.Contains(t, msg, filepath.Join("sites-extra", "portal.conf"))
}

// --combine collapses everything into one File, and Node.File keeps the real
// origin. Filtering has to follow the node, or --file would be unusable with
// --combine and would answer "matches no file" for a file that is right there.
func TestInspectFileFiltersACombinedTreeByTheNodesOrigin(t *testing.T) {
	code, _, raw := runInspect(t, "inspect", "--combine",
		"--file", filepath.Join("sites-extra", "portal.conf"), "-c", filterFixture(t))
	require.Equal(t, output.ExitOK, code)

	r := decodeFilter(t, raw)
	servers := serversIn(r)
	require.Len(t, servers, 1)
	require.Contains(t, servers[0].File, filepath.Join("sites-extra", "portal.conf"))
}

// --- --server ---------------------------------------------------------------

// Ambiguity is over the NAME, not over the number of blocks: portal.example.com
// is served by a :80 block and a :443 block, which is ordinary nginx and not a
// question to put back to the caller.
func TestInspectServerEmitsEveryBlockThatDeclaresTheName(t *testing.T) {
	code, _, raw := runInspect(t, "inspect",
		"--server", "portal.example.com", "-c", filterFixture(t))
	require.Equal(t, output.ExitOK, code)

	servers := serversIn(decodeFilter(t, raw))
	require.Len(t, servers, 2)
}

// Without this rule the exact name would be permanently ambiguous against
// every name that contains it, and therefore unusable.
func TestInspectServerExactNameBeatsTheFragment(t *testing.T) {
	code, _, raw := runInspect(t, "inspect",
		"--server", "portal.example.com", "-c", filterFixture(t))
	require.Equal(t, output.ExitOK, code)

	for _, s := range serversIn(decodeFilter(t, raw)) {
		for _, child := range s.Block {
			if child.Directive != "server_name" {
				continue
			}
			require.Equal(t, []string{"portal.example.com"}, child.Args)
		}
	}
}

func TestInspectServerAmbiguityListsTheCandidatesAndRefuses(t *testing.T) {
	code, env, _ := runInspect(t, "inspect", "--server", "portal", "-c", filterFixture(t))

	require.Equal(t, output.ExitUsage, code)
	require.Equal(t, "NGX-0103", env.Diagnostics[0].Code)
	require.Contains(t, env.Diagnostics[0].Message, "portal.example.com")
	require.Contains(t, env.Diagnostics[0].Message, "portal-admin.example.com")
}

func TestInspectServerNoMatchListsTheDeclaredNames(t *testing.T) {
	code, env, _ := runInspect(t, "inspect", "--server", "absent.example.com", "-c", filterFixture(t))

	require.Equal(t, output.ExitUsage, code)
	require.Equal(t, "NGX-0104", env.Diagnostics[0].Code)
	require.Contains(t, env.Diagnostics[0].Message, "legacy.example.com")
	require.Contains(t, env.Diagnostics[0].Message, "portal-admin.example.com")
}

// The matched block comes back where it was, inside http, so the id an agent
// reads here is the same id the unfiltered read would have given it.
func TestInspectServerKeepsTheAncestorChainAndTheIDs(t *testing.T) {
	code, _, raw := runInspect(t, "inspect",
		"--server", "legacy.example.com", "-c", filterFixture(t))
	require.Equal(t, output.ExitOK, code)

	r := decodeFilter(t, raw)
	require.Len(t, r.Data.Config, 1)
	top := r.Data.Config[0].Parsed
	require.Len(t, top, 1)
	require.Equal(t, "http", top[0].Directive, "the server comes back inside its http block")

	servers := serversIn(r)
	require.Len(t, servers, 1)
	require.Equal(t, "h.s0", servers[0].ID)
}

// --- --file and --server together -------------------------------------------

// AND, documented and tested, because the other reading is equally plausible.
func TestInspectFileAndServerCombineWithAND(t *testing.T) {
	code, _, raw := runInspect(t, "inspect",
		"--file", filepath.Join("sites", "portal.conf"),
		"--server", "portal.example.com", "-c", filterFixture(t))
	require.Equal(t, output.ExitOK, code)

	r := decodeFilter(t, raw)
	require.Len(t, r.Data.Config, 1)
	require.Len(t, serversIn(r), 2)
	require.Contains(t, filesOf(r)[0], filepath.Join("sites", "portal.conf"))
}

// The proof that it is AND and not OR: the name exists in the configuration,
// but not in the file that was named, so the answer is a refusal -- and the
// names it offers are the ones of the narrowed scope, not of the whole tree.
func TestInspectServerIsScopedByTheFileFilter(t *testing.T) {
	code, env, _ := runInspect(t, "inspect",
		"--file", filepath.Join("sites-extra", "portal.conf"),
		"--server", "portal.example.com", "-c", filterFixture(t))

	require.Equal(t, output.ExitUsage, code)
	require.Equal(t, "NGX-0104", env.Diagnostics[0].Code)
	require.Contains(t, env.Diagnostics[0].Message, "portal-admin.example.com")
	require.NotContains(t, env.Diagnostics[0].Message, "legacy.example.com",
		"the scope of --server is what --file already narrowed to")
}

// --- partiality and the hash ------------------------------------------------

// H2: config.Hash is computed over the tree it is handed, so the hash of a
// filtered tree is a valid hash OF A SUBSET, indistinguishable from the hash of
// the whole. Harmless while reading, and a corrupted write the moment v0.2 uses
// it for optimistic locking.
func TestInspectFilteredResultOmitsTheConfigHash(t *testing.T) {
	_, env, raw := runInspect(t, "inspect",
		"--file", filepath.Join("sites", "portal.conf"), "-c", filterFixture(t))

	require.Empty(t, env.Meta.ConfigHash,
		"a filtered answer must never look authoritative about a whole it never saw")

	scope := decodeFilter(t, raw).Data.Scope
	require.NotNil(t, scope)
	require.True(t, scope.ConfigHashOmitted,
		"the absence has to be explained where the agent is looking, not left to be guessed")
}

func TestInspectUnfilteredResultCarriesTheConfigHash(t *testing.T) {
	_, env, _ := runInspect(t, "inspect", "--full-tree", "-c", filterFixture(t))
	require.Contains(t, env.Meta.ConfigHash, "sha256:")
}

// R2b: the marker lives INSIDE data, because an agent reading only data has to
// trip over the gap exactly where it is looking.
func TestInspectPartialityIsMarkedInsideData(t *testing.T) {
	_, env, raw := runInspect(t, "inspect",
		"--server", "portal.example.com", "-c", filterFixture(t))

	scope := decodeFilter(t, raw).Data.Scope
	require.NotNil(t, scope)
	require.True(t, scope.Partial)
	require.Equal(t, "portal.example.com", scope.Filters.Server)
	require.Empty(t, scope.Filters.File, "a filter that was not used is omitted, not empty")
	require.Equal(t, 1, scope.FilesEmitted)

	// The summary still describes the WHOLE configuration that was read:
	// that is what makes it comparable between a filtered call and an
	// unfiltered one, and files_emitted is what says how much came out.
	r := decodeFilter(t, raw)
	require.Equal(t, 3, r.Data.Summary.Files)
	require.Equal(t, 4, r.Data.Summary.Servers)

	require.True(t, env.OK, "a subset asked for is not a failure")
	require.Len(t, env.Diagnostics, 1)
	require.Equal(t, output.SeverityInfo, env.Diagnostics[0].Severity)
	require.Equal(t, "NGX-0105", env.Diagnostics[0].Code)
}

// Redaction runs over the filtered tree like over any other: the filters must
// not become a way around it.
func TestInspectFilteredResultIsStillRedacted(t *testing.T) {
	_, _, raw := runInspect(t, "inspect",
		"--server", "portal.example.com", "-c", filterFixture(t))

	require.NotContains(t, raw, "/etc/ssl/private/portal.key")
	require.Contains(t, raw, output.RedactedValue)
}
