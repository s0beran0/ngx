// Package language_test enforces the rule that this repository is written in
// English, by checking rather than by remembering.
//
// The rule has been in CLAUDE.md since the repository was translated, and it
// was broken the very next day, in four files, by the same author who wrote
// it. Not through disagreement: the translation was committed as "finish
// translating the repository", which reads as a migration that ended, and
// attention moved on. Nothing looked afterwards.
//
// That is the defect this test closes, and it is the same shape as two others
// already recorded here: `goreleaser check` approves a template calling a
// function that does not exist, and `-ldflags -X` silently ignores a symbol
// that is not there. A check that cannot fail and a check that does not exist
// are indistinguishable from the outside -- both are green.
//
// It is deliberately a test and not a CI-only script, so that it runs in the
// same `go test ./...` that everything else runs in. A check nobody runs
// locally is found in CI, after the commit, which is the wrong end.
package language_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// markers are Portuguese words that are not English words. Matching is
// whole-word and case-insensitive.
//
// The list is short on purpose. Its job is to catch prose written in the wrong
// language, which is never one word long -- a paragraph in Portuguese trips
// several of these. Chasing completeness would mean adding short words that
// collide with English and with the vocabulary of this domain, and a check
// that cries wolf gets deleted.
//
// Deliberately EXCLUDED, each for a reason that already bit or would:
//
//	"ate"        -- English past tense of "eat"
//	"no", "na"   -- "no" is English; "na" appears in identifiers
//	"os", "as"   -- "OS" is the operating system; "as" is English
//	"do", "da"   -- "do" is English; both appear in names
//	"de", "em"   -- "em" is a typographic unit; "de" appears in names
//	"so", "era"  -- both are English
//	"com"        -- every .com domain would match
//	"um"         -- "um" is an English interjection
//
// Accents are not what is checked, and could not be: CLAUDE.md requires code
// comments to carry no accents, so Portuguese here is unaccented by rule. The
// markers below are all accent-free for that reason.
var markers = []string{
	"nao", "entao", "porque", "pois", "apenas", "tambem", "ainda",
	"quando", "onde", "qual", "quais", "sobre", "entre", "depois",
	"antes", "agora", "aqui", "assim", "cada", "como", "mais",
	"pode", "podem", "deve", "devem", "precisa", "precisam",
	"isso", "esse", "essa", "esta", "estao", "nenhum", "nenhuma",
	"mesmo", "mesma", "muito", "sempre", "nunca", "tudo", "nada",
	"algo", "outro", "outra", "certo", "errado", "vez", "vezes",
	"caso", "casos", "arquivo", "arquivos", "linha", "linhas",
	"valor", "valores", "chave", "saida", "entrada", "leitura",
	"escrita", "erro", "erros", "falha", "teste", "testes",
	"para", "pelo", "pela", "seu", "sua", "seus", "suas",
	"uma", "umas", "sao", "foi", "ser", "ter", "tem", "faz",
	"fazer", "quer", "sabe", "coisa", "forma", "lugar", "tempo",
	"corpo", "recusa", "aceita", "traducao", "resolucao", "primeiro",
	"permissao",
}

// allowed records the places a marker is legitimately present, because the
// text is ABOUT the Portuguese word rather than written in Portuguese.
//
// Every entry names a file and a word, and the check fails if an entry stops
// matching anything -- an allowlist that outlives its reason is how a rule
// quietly stops applying.
var allowed = map[string][]string{
	// CLAUDE.md tells the story of a diagnostic that branched on the word
	// "permissao" and broke when the repository was translated. Quoting the
	// word is the point of the story.
	"CLAUDE.md": {"permissao"},
}

func TestTheRepositoryIsWrittenInEnglish(t *testing.T) {
	root := repoRoot(t)
	pattern := regexp.MustCompile(`(?i)\b(` + strings.Join(markers, "|") + `)\b`)

	usedAllowance := map[string]bool{}
	var found []string

	for _, rel := range trackedFiles(t, root) {
		path := filepath.Join(root, rel)
		switch filepath.Ext(rel) {
		case ".go":
			found = append(found, scanGoComments(t, path, rel, pattern, usedAllowance)...)
		case ".md":
			found = append(found, scanLines(t, path, rel, pattern, usedAllowance)...)
		}
	}

	if len(found) > 0 {
		t.Errorf("this repository is written in English (CLAUDE.md), and %d line(s) are not:\n%s",
			len(found), strings.Join(found, "\n"))
	}

	// An allowance that matches nothing is a rule that has quietly stopped
	// applying somewhere. Failing here forces the entry to be removed rather
	// than left as cover for a future violation.
	for file, words := range allowed {
		for _, w := range words {
			require.Truef(t, usedAllowance[file+":"+w],
				"the allowance for %q in %s no longer matches anything; remove it", w, file)
		}
	}
}

// trackedFiles asks git what belongs to the repository, instead of walking the
// disk and skipping directories by name.
//
// The first version of this test carried a hand-written skip list, and it was
// wrong on its first run: .superpowers/ is local tool state, gitignored, and
// 4277 of the 4302 hits came from there. A list of directories to ignore has
// to be updated by whoever adds the next one, which is the same "somebody will
// remember" that this test exists to replace. What the repository IS, git
// already knows.
func trackedFiles(t *testing.T, root string) []string {
	t.Helper()

	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	out, err := cmd.Output()
	require.NoError(t, err, "git ls-files failed; this test needs a git checkout")

	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	require.NotEmpty(t, files, "git reported no tracked files")
	return files
}

// scanGoComments reads comments through the parser rather than by grepping the
// file. A string literal holding a configuration snippet, an nginx directive
// or a test fixture is not prose, and matching it would produce failures that
// cannot be fixed.
func scanGoComments(t *testing.T, path, rel string, pattern *regexp.Regexp, used map[string]bool) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		// A file that does not parse is not this test's problem; the build
		// reports it, and reporting it twice helps nobody.
		return nil
	}

	var found []string
	for _, group := range file.Comments {
		for _, c := range group.List {
			if hit := match(rel, c.Text, pattern, used); hit != "" {
				found = append(found, rel+":"+itoa(fset.Position(c.Pos()).Line)+": comment: "+hit)
			}
		}
	}

	// Identifiers too, and this is not thoroughness for its own sake: the gap
	// was found by this test passing a file whose test function was called
	// TestSpanDeArgumentoEntreAspasCobreAsAspas. Comments were clean, the name
	// was not, and CLAUDE.md requires English of both.
	//
	// A camelCase or PascalCase name is split before matching, so
	// "SpanDeArgumento" is seen as the words it is made of. Without the split
	// no marker would ever match an identifier, and this whole branch would be
	// decoration that always passes.
	ast.Inspect(file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if hit := match(rel, splitCamel(id.Name), pattern, used); hit != "" {
			found = append(found, rel+":"+itoa(fset.Position(id.Pos()).Line)+": identifier "+id.Name+": "+hit)
		}
		return true
	})
	return found
}

// splitCamel turns "SpanDeArgumentoEntreAspas" into "Span De Argumento Entre
// Aspas", so whole-word matching can see the words inside a name.
func splitCamel(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte(' ')
		}
		if r == '_' {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func scanLines(t *testing.T, path, rel string, pattern *regexp.Regexp, used map[string]bool) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var found []string
	for i, line := range strings.Split(string(data), "\n") {
		if hit := match(rel, line, pattern, used); hit != "" {
			found = append(found, rel+":"+itoa(i+1)+": "+hit)
		}
	}
	return found
}

// match returns a description of the violation, or "" when the line is clean
// or entirely covered by an allowance.
func match(rel, text string, pattern *regexp.Regexp, used map[string]bool) string {
	hits := pattern.FindAllString(text, -1)
	if len(hits) == 0 {
		return ""
	}

	var real []string
	for _, h := range hits {
		if isAllowed(rel, h, used) {
			continue
		}
		real = append(real, strings.ToLower(h))
	}
	if len(real) == 0 {
		return ""
	}
	return strings.Join(dedupe(real), ", ")
}

func isAllowed(rel, hit string, used map[string]bool) bool {
	for _, w := range allowed[rel] {
		if strings.EqualFold(w, hit) {
			used[rel+":"+w] = true
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// repoRoot walks up until it finds go.mod, so the test does not depend on
// where it was invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir, "go.mod not found above %s", dir)
		dir = parent
	}
}
