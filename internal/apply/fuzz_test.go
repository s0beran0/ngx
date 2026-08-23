package apply_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s0beran0/ngx/internal/apply"
	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/plan"
)

// FuzzWrite generates edits against generated configurations and checks the
// three things a write must never do.
//
// The property is NOT "it does not crash". A write that corrupts a file exits
// zero and looks like a success, so the assertions are about the BYTES:
//
//	the file is either exactly the intended result or exactly the original --
//	never something in between, and never truncated;
//
//	nothing outside the edited span changes, which is checked by rebuilding the
//	expected content independently of the code under test;
//
//	a rollback restores the original byte for byte.
//
// Rebuilding the expectation here rather than asking apply what it did is the
// point. Comparing apply's output against apply's own idea of the result would
// be the tautology this project has already thrown a fuzz away over.
func FuzzWrite(f *testing.F) {
	f.Add("events { worker_connections 16; }\nhttp { server { listen 8080; } }\n", 0, "9090", true)
	f.Add("events {}\nhttp {\n  server {\n    listen 80;\n    server_name a;\n  }\n}\n", 1, "b.test", true)
	f.Add("events {}\nhttp { server { listen 80; } }\n", 0, "", true)
	f.Add("events {}\r\nhttp {\r\n  server {\r\n    listen 80;\r\n  }\r\n}\r\n", 0, "8080", false)
	f.Add("# c\nevents {}\nhttp { server { listen 80; add_header X \"a; b\"; } }\n", 1, "y", true)
	f.Add("events {}\nhttp { server { listen 80; } }\n", 5, "x", true)

	f.Fuzz(func(t *testing.T, src string, which int, replacement string, accept bool) {
		if len(src) > 4096 || which < 0 || which > 64 || len(replacement) > 128 {
			return
		}
		if strings.ContainsAny(replacement, "\x00") {
			return
		}

		dir := t.TempDir()
		root := filepath.Join(dir, "nginx.conf")
		if err := os.WriteFile(root, []byte(src), 0o644); err != nil {
			t.Skip()
		}
		tree, err := config.Parse(config.ParseOptions{Path: root})
		if err != nil || len(tree.Files) == 0 {
			return
		}

		// Pick a plain directive by index. Anything else -- a block, a comment,
		// a node with no head -- is out of scope for a substitution over
		// HeadSpan.
		var targets []*config.Node
		tree.Walk(func(n *config.Node) bool {
			if !n.IsComment() && !n.HasBlock() && n.HeadSpan.Len() > 0 && n.ArgSpans != nil {
				targets = append(targets, n)
			}
			return true
		})
		if len(targets) == 0 {
			return
		}
		target := targets[which%len(targets)]

		source := tree.Files[0].Source
		if target.File != root {
			return
		}

		before := string(source[target.HeadSpan.Start:target.HeadSpan.End])
		after := target.Directive
		if replacement != "" {
			after += " " + replacement
		}

		p := plan.Plan{
			Root:       root,
			ConfigHash: tree.Hash,
			Edits: []plan.Edit{{
				File: target.File, Ref: target.Ref, Span: target.HeadSpan,
				Before: before, After: after,
			}},
		}
		if err := p.Validate(); err != nil {
			return // a plan this test built badly is not a finding about apply
		}

		// The expectation, built here, from the ORIGINAL bytes and the span.
		want := string(source[:target.HeadSpan.Start]) + after + string(source[target.HeadSpan.End:])

		res, applyErr := apply.Run(apply.Options{
			Plan: &p, Tree: tree, Root: root,
			Validate: func() error {
				if accept {
					return nil
				}
				return errRefused
			},
		})

		got, readErr := os.ReadFile(root)
		if readErr != nil {
			t.Fatalf("the file is gone after an apply: %v", readErr)
		}

		switch {
		case accept:
			if applyErr != nil {
				t.Fatalf("an accepted write failed: %v", applyErr)
			}
			if string(got) != want {
				t.Fatalf("the file is not the intended result.\nwant %q\ngot  %q", want, got)
			}
		default:
			if applyErr == nil {
				t.Fatalf("a refused configuration was kept")
			}
			if string(got) != src {
				t.Fatalf("the rollback did not restore the original.\nwant %q\ngot  %q", src, got)
			}
		}

		// Nothing left behind, ever. A directory of temporary files is how an
		// operator finds this package the hard way.
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".ngx-apply-") {
				t.Fatalf("a temporary file was left behind: %s", e.Name())
			}
		}

		// And every list is a list, on every path.
		for name, list := range map[string][]string{
			"written": res.Written, "rolled_back": res.RolledBack,
			"created": res.Created, "deleted": res.Deleted, "not_restored": res.NotRestored,
		} {
			if list == nil {
				t.Fatalf("%s came back nil", name)
			}
		}
	})
}

var errRefused = errors.New("nginx refused it")
