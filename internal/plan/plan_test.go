package plan_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/plan"
)

// fixture writes a configuration, parses it, and builds a plan that changes one
// directive's value -- the shape every test here starts from.
func fixture(t *testing.T, src string) (*config.Tree, string, plan.Plan) {
	t.Helper()

	dir := t.TempDir()
	root := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(root, []byte(src), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)

	var target *config.Node
	tree.Walk(func(n *config.Node) bool {
		if target == nil && n.Directive == "listen" {
			target = n
		}
		return true
	})
	require.NotNil(t, target, "the fixture needs a listen directive")

	head := string(tree.Files[0].Source[target.HeadSpan.Start:target.HeadSpan.End])
	return tree, root, plan.Plan{
		Root:       root,
		ConfigHash: tree.Hash,
		Edits: []plan.Edit{{
			File:   target.File,
			Ref:    target.Ref,
			Span:   target.HeadSpan,
			Before: head,
			After:  "listen 8443 ssl",
			Reason: "set listen",
		}},
	}
}

const simple = "events { worker_connections 16; }\n" +
	"http {\n  server {\n    listen 8080;\n    server_name a.test;\n  }\n}\n"

func TestAWellFormedPlanVerifiesAgainstWhatItWasBuiltFrom(t *testing.T) {
	tree, root, p := fixture(t, simple)

	require.NoError(t, p.Validate())
	require.NoError(t, p.Verify(tree, root))
	require.Equal(t, []string{root}, p.Files())
}

// Every refusal, provoked. A refusal that was never observed is a claim: this
// project has recorded four checks that could only ever pass, and this package
// is the one standing between a plan and somebody's configuration.
func TestEveryRefusalCanHappen(t *testing.T) {
	t.Run("no root", func(t *testing.T) {
		_, _, p := fixture(t, simple)
		p.Root = ""
		requireRefusal(t, p.Validate(), plan.RefusalMalformed)
	})

	t.Run("no hash", func(t *testing.T) {
		_, _, p := fixture(t, simple)
		p.ConfigHash = ""
		requireRefusal(t, p.Validate(), plan.RefusalMalformed)
	})

	t.Run("no edits", func(t *testing.T) {
		_, _, p := fixture(t, simple)
		p.Edits = nil
		requireRefusal(t, p.Validate(), plan.RefusalMalformed)
	})

	t.Run("edit with no ref", func(t *testing.T) {
		_, _, p := fixture(t, simple)
		p.Edits[0].Ref = ""
		requireRefusal(t, p.Validate(), plan.RefusalMalformed)
	})

	t.Run("inverted span", func(t *testing.T) {
		_, _, p := fixture(t, simple)
		p.Edits[0].Span.Start, p.Edits[0].Span.End = p.Edits[0].Span.End, p.Edits[0].Span.Start
		requireRefusal(t, p.Validate(), plan.RefusalMalformed)
	})

	// The invariant that makes "a plan never contains a rendered file"
	// checkable: Before has to be exactly the bytes at the span, so it cannot
	// hold a whole file.
	t.Run("before longer than its span", func(t *testing.T) {
		_, _, p := fixture(t, simple)
		p.Edits[0].Before += " and the rest of the file"
		requireRefusal(t, p.Validate(), plan.RefusalMalformed)
	})

	t.Run("two edits over the same bytes", func(t *testing.T) {
		_, _, p := fixture(t, simple)
		p.Edits = append(p.Edits, p.Edits[0])
		requireRefusal(t, p.Validate(), plan.RefusalMalformed)
	})

	t.Run("applied against another root", func(t *testing.T) {
		tree, _, p := fixture(t, simple)
		requireRefusal(t, p.Verify(tree, "/somewhere/else/nginx.conf"), plan.RefusalWrongRoot)
	})

	t.Run("configuration changed since the plan", func(t *testing.T) {
		tree, root, p := fixture(t, simple)
		p.ConfigHash = "0000000000000000000000000000000000000000000000000000000000000000"
		requireRefusal(t, p.Verify(tree, root), plan.RefusalStaleHash)
	})

	t.Run("edit targets a file that was not read", func(t *testing.T) {
		tree, root, p := fixture(t, simple)
		p.Edits[0].File = filepath.Join(filepath.Dir(root), "other.conf")
		requireRefusal(t, p.Verify(tree, root), plan.RefusalBytesMoved)
	})

	t.Run("span past the end of the file", func(t *testing.T) {
		tree, root, p := fixture(t, simple)
		p.Edits[0].Span.End = len(tree.Files[0].Source) + 10
		p.Edits[0].Before = strings.Repeat("x", p.Edits[0].Span.Len())
		requireRefusal(t, p.Verify(tree, root), plan.RefusalBytesMoved)
	})

	t.Run("bytes moved under an agreeing hash", func(t *testing.T) {
		tree, root, p := fixture(t, simple)
		// The hash still matches -- it is copied from this very tree -- and
		// only the edit's own expectation is wrong. This is the case the hash
		// cannot see, and the reason Before exists.
		p.Edits[0].Before = strings.Repeat("z", p.Edits[0].Span.Len())
		requireRefusal(t, p.Verify(tree, root), plan.RefusalBytesMoved)
	})
}

// The two anchors answer different questions, and this is the case that shows
// neither one is enough on its own.
//
// The hash covers base names rather than paths (config/hash.go), so a copy of a
// configuration in another directory hashes IDENTICALLY. Without the root
// anchor, a plan built against one would verify against the other and write to
// the wrong tree.
func TestACopyInAnotherDirectoryHashesTheSameAndIsStillRefused(t *testing.T) {
	_, _, p := fixture(t, simple)

	// The same bytes, the same file name, a different directory.
	copyDir := t.TempDir()
	copyRoot := filepath.Join(copyDir, "nginx.conf")
	require.NoError(t, os.WriteFile(copyRoot, []byte(simple), 0o644))

	copyTree, err := config.Parse(config.ParseOptions{Path: copyRoot})
	require.NoError(t, err)

	require.Equal(t, p.ConfigHash, copyTree.Hash,
		"the premise of this test: the hash does not distinguish the two, by design")

	requireRefusal(t, p.Verify(copyTree, copyRoot), plan.RefusalWrongRoot)
}

// A plan is data that crosses a process boundary: an agent writes it to a file
// or a pipe and applies it later. It has to survive that trip exactly.
func TestAPlanSurvivesJSONUnchanged(t *testing.T) {
	tree, root, p := fixture(t, simple)

	raw, err := json.Marshal(p)
	require.NoError(t, err)

	var back plan.Plan
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, p, back)
	require.NoError(t, back.Verify(tree, root),
		"a plan that made the round trip still has to verify")
}

// The structural form of D1, asserted rather than described: nothing in a
// serialised plan is anywhere near the size of the file it changes.
func TestAPlanIsSmallerThanTheFileItChanges(t *testing.T) {
	_, _, p := fixture(t, simple)

	raw, err := json.Marshal(p)
	require.NoError(t, err)

	// The edit replaces one directive's head, so the plan's own byte count is
	// dominated by paths and field names, never by content. The comparison
	// that matters is against the file: a plan that carried a rendered file
	// could not be smaller than it.
	require.Less(t, len(p.Edits[0].Before)+len(p.Edits[0].After), len(simple),
		"the edit carries more bytes than the file it changes, which means it is "+
			"holding content rather than describing a substitution")
	require.NotContains(t, string(raw), "worker_connections",
		"the plan carries bytes from a part of the configuration it does not touch")
}

func TestDescribeNamesEveryEditWithoutDumpingBytes(t *testing.T) {
	_, _, p := fixture(t, simple)
	out := p.Describe()

	require.Contains(t, out, p.Edits[0].Ref, "a reviewer needs to know what is changing")
	require.Contains(t, out, "listen 8443 ssl")
	require.NotContains(t, out, "worker_connections")
}

// The refusals above are asserted by CODE and never by message text, which is
// the project's rule: a caller branches on the class, and wording changes. This
// one test looks at the prose, and it checks a PROPERTY of it rather than a
// phrasing -- that a refusal says enough for an operator to act.
//
// It is here because the two anchors are easy to confuse. An operator who reads
// "the configuration changed" does something different from one who reads "this
// is a different configuration", and a refusal that does not distinguish them
// sends them to re-read a file that was never the problem.
func TestARefusalSaysEnoughToActOn(t *testing.T) {
	tree, root, p := fixture(t, simple)

	copyDir := t.TempDir()
	copyRoot := filepath.Join(copyDir, "nginx.conf")
	require.NoError(t, os.WriteFile(copyRoot, []byte(simple), 0o644))
	copyTree, err := config.Parse(config.ParseOptions{Path: copyRoot})
	require.NoError(t, err)

	wrongRoot := p.Verify(copyTree, copyRoot)
	require.Error(t, wrongRoot)
	require.Contains(t, wrongRoot.Error(), p.Root,
		"a wrong-root refusal has to name the root the plan was built against")
	require.Contains(t, wrongRoot.Error(), copyRoot,
		"and the one it was applied against, or the operator cannot tell them apart")

	// The SAME root, so the wrong-root check passes and the hash is what fails.
	// Getting this wrong the first time is the point of the assertion below: the
	// two refusals are easy to confuse even while writing the test for them.
	stale := p
	stale.ConfigHash = strings.Repeat("0", 64)
	staleErr := stale.Verify(tree, root)
	require.Error(t, staleErr)
	require.NotContains(t, staleErr.Error(), stale.Root,
		"a stale-hash refusal is about time, not about place: naming the path would "+
			"read as a wrong-root problem and send the operator looking in the wrong place")
}

func requireRefusal(t *testing.T, err error, want plan.RefusalCode) {
	t.Helper()

	require.Error(t, err, "expected a refusal with code %q and got none", want)
	got, ok := plan.CodeOf(err)
	require.Truef(t, ok, "the error is not a plan refusal: %v", err)
	require.Equalf(t, want, got, "wrong refusal code for: %v", err)
}

// The codes are a contract: they go into the JSON envelope and a caller
// branches on them. Asserting the literals is what keeps a rename from
// silently changing what consumers see -- the rule this project states as
// "lock contracts with literal values".
func TestRefusalCodesAreLiterals(t *testing.T) {
	require.Equal(t, "plan_malformed", string(plan.RefusalMalformed))
	require.Equal(t, "plan_wrong_root", string(plan.RefusalWrongRoot))
	require.Equal(t, "plan_stale_hash", string(plan.RefusalStaleHash))
	require.Equal(t, "plan_bytes_moved", string(plan.RefusalBytesMoved))
}

// FuzzPlanValidation asserts the invariant that matters most about Validate:
// it never ACCEPTS a plan whose edits could corrupt a file.
//
// The property is not "no crash". A malformed plan that Validate lets through
// reaches the write path, and the write path trusts it -- that is the whole
// point of separating the two. So the assertion is that anything Validate
// accepts satisfies, independently re-checked here, the three things apply
// depends on: spans in range, Before exactly as long as its span, and no two
// edits over the same bytes.
//
// Re-checking rather than trusting is deliberate. If the invariant were read
// off the same code that enforces it, this would prove only self-consistency --
// the tautology this project has thrown a fuzz away over.
func FuzzPlanValidation(f *testing.F) {
	f.Add("a.conf", 0, 5, "abcde", "x") // well formed
	f.Add("a.conf", 5, 5, "", "inserted")
	f.Add("a.conf", 0, 0, "", "")
	f.Add("a.conf", -1, 3, "abc", "y") // negative offset
	f.Add("a.conf", 7, 2, "ab", "y")   // inverted
	f.Add("", 0, 1, "a", "b")          // no file

	// Before shorter, and longer, than its span. These two seeds exist because
	// they were MISSING: relaxing the length check in Validate on purpose left
	// the corpus above entirely green, since every one of its entries is either
	// well formed or refused by an earlier rule. A negative verification that
	// passes for want of a seed proves nothing about the fuzz.
	f.Add("a.conf", 0, 5, "abc", "x")
	f.Add("a.conf", 0, 2, "abcdef", "x")

	f.Fuzz(func(t *testing.T, file string, start, end int, before, after string) {
		// Keep the offsets in a range where arithmetic cannot overflow: the
		// property is about the rules, not about int64 edges.
		if start < -1000 || start > 1_000_000 || end < -1000 || end > 1_000_000 {
			return
		}

		p := plan.Plan{
			Root:       "/etc/nginx/nginx.conf",
			ConfigHash: strings.Repeat("a", 64),
			Edits: []plan.Edit{{
				File:   file,
				Ref:    file + "#s0",
				Span:   config.Span{Start: start, End: end},
				Before: before,
				After:  after,
			}},
		}

		if err := p.Validate(); err != nil {
			// A refusal has to be a refusal, with a code a caller can branch
			// on. An error that is not one would reach the CLI as an internal
			// failure and lose its exit code.
			if _, ok := plan.CodeOf(err); !ok {
				t.Fatalf("Validate rejected a plan with an error that carries no code: %v", err)
			}
			return
		}

		// Accepted. Everything apply depends on has to hold.
		if file == "" {
			t.Fatalf("accepted an edit with no file")
		}
		if start < 0 || end < start {
			t.Fatalf("accepted an impossible span [%d,%d)", start, end)
		}
		if len(before) != end-start {
			t.Fatalf("accepted before-text of %d byte(s) for a span of %d",
				len(before), end-start)
		}
	})
}
