package apply

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/plan"
)

// Preflight asks nginx whether it would accept a plan, WITHOUT touching a
// single file of the real configuration.
//
// The naive way to do this is unsound and was measured before this existed:
// copy the tree somewhere else and run `nginx -t` on the copy. With an absolute
// include -- the default layout on RHEL and Debian -- nginx follows the absolute
// path back to the ORIGINAL files, reports "syntax is ok", and never reads the
// change at all. A pre-flight that passes without looking is worse than none.
//
// What makes it sound is rewriting the includes. The shadow mirrors each file at
// its full path under a temporary root, so /etc/nginx/conf.d/a.conf becomes
// <shadow>/etc/nginx/conf.d/a.conf, and every ABSOLUTE include is rewritten to
// point inside the shadow. Relative includes need no rewriting: they already
// resolve against the directory they sit in, which in the shadow is the copy.
//
// Two things are deliberately NOT changed, and they are why this answers the
// real question rather than an easier one:
//
//	the prefix stays nginx's own, so log paths, the pid file and anything else
//	relative behaves as it does in production;
//
//	every path that is not an include -- root, ssl_certificate, auth_basic_user_file
//	-- stays absolute and points at the REAL file, because those are not being
//	changed and their existence is part of what nginx is being asked about.
//
// The rewrite itself goes through the same span substitution as any edit, using
// the include's ArgSpans[0]. There is no second code path for modifying a file,
// which is what keeps the two from drifting.
type PreflightOptions struct {
	Plan *plan.Plan
	Tree *config.Tree
	Root string

	// ValidateAt is given the path of the shadow's top-level configuration and
	// answers whether nginx accepts it. In production it runs
	// `nginx -t -c <that path>`.
	ValidateAt func(configPath string) error
}

// PreflightResult says what nginx thought, and where the shadow was, so a
// failure can be inspected before it is cleaned up.
type PreflightResult struct {
	// Accepted is nginx's verdict on the configuration the plan would produce.
	Accepted bool `json:"accepted"`

	// Reason is what nginx said when it refused.
	Reason string `json:"reason,omitempty"`

	// Files are the real paths the plan would change, so the answer names what
	// it is about.
	Files []string `json:"files"`
}

// Preflight builds the shadow, validates it, and removes it.
func Preflight(opts PreflightOptions) (*PreflightResult, error) {
	if opts.Plan == nil || opts.Tree == nil || opts.ValidateAt == nil {
		return nil, &Failure{Code: CodeVerifyFailed,
			Message: "preflight was called without a plan, a tree or a validator"}
	}

	if err := opts.Plan.Verify(opts.Tree, opts.Root); err != nil {
		return nil, &Failure{
			Code:    CodeVerifyFailed,
			Message: "the plan does not describe this configuration: " + err.Error(),
			Result:  newResult(nil, nil, nil),
			Cause:   err,
		}
	}

	shadow, err := os.MkdirTemp("", "ngx-preflight-*")
	if err != nil {
		return nil, fmt.Errorf("cannot create a directory for the pre-flight: %w", err)
	}
	defer os.RemoveAll(shadow)

	rewrites, err := includeRewrites(opts.Tree, shadow)
	if err != nil {
		return nil, err
	}

	// The plan's own edits and the include rewrites are applied TOGETHER, by
	// the same function that computes what apply would write. Building the
	// shadow with a second mechanism would mean pre-flighting something other
	// than what apply produces, which is the one thing this must not do.
	combined := *opts.Plan
	combined.Edits = append(append([]plan.Edit{}, opts.Plan.Edits...), rewrites...)
	if err := combined.Validate(); err != nil {
		return nil, &Failure{Code: CodeVerifyFailed, Result: newResult(nil, nil, nil), Cause: err,
			Message: "the include rewrites collide with the plan's own edits: " + err.Error()}
	}

	pending, err := contents(&combined, opts.Tree)
	if err != nil {
		return nil, &Failure{Code: CodeVerifyFailed, Result: newResult(nil, nil, nil), Cause: err,
			Message: err.Error()}
	}

	// Every file of the configuration, edited or not: nginx reads the whole
	// tree, so a shadow missing the untouched files would be a different
	// configuration.
	for _, f := range opts.Tree.Files {
		body, ok := pending[f.Path]
		if !ok {
			body = f.Source
		}
		if err := writeShadow(shadow, f.Path, body); err != nil {
			return nil, err
		}
	}

	// Files the plan creates exist in the shadow too, since nginx would load
	// them; files it deletes are simply not written there.
	deleting := map[string]bool{}
	for _, d := range opts.Plan.Deletes {
		deleting[d.File] = true
	}
	for _, c := range opts.Plan.Creates {
		if err := writeShadow(shadow, c.File, []byte(c.Content)); err != nil {
			return nil, err
		}
	}
	for path := range deleting {
		_ = os.Remove(shadowPath(shadow, path))
	}

	res := &PreflightResult{Files: opts.Plan.Files()}
	if err := opts.ValidateAt(shadowPath(shadow, opts.Root)); err != nil {
		res.Accepted = false
		res.Reason = err.Error()
		return res, nil
	}
	res.Accepted = true
	return res, nil
}

// includeRewrites turns every ABSOLUTE include into one pointing inside the
// shadow.
//
// A relative include is left alone: it resolves against the directory of the
// file that declares it, and in the shadow that directory holds the copy. A
// rewrite there would be work that changes nothing, and every unnecessary
// change to the text is one more difference between what is validated and what
// will be written.
func includeRewrites(tree *config.Tree, shadow string) ([]plan.Edit, error) {
	var out []plan.Edit
	for _, f := range tree.Files {
		var walkErr error
		walkNodes(f.Nodes, func(n *config.Node) {
			if walkErr != nil || n.Directive != "include" || len(n.Args) == 0 {
				return
			}
			if !filepath.IsAbs(n.Args[0]) {
				return
			}
			if n.ArgSpans == nil || len(n.ArgSpans) == 0 {
				walkErr = &Failure{Code: CodeVerifyFailed,
					Message: fmt.Sprintf(
						"the include at %s:%d has no byte range, so a pre-flight cannot "+
							"redirect it into the shadow tree. Use `apply --check` instead",
						f.Path, n.Line)}
				return
			}

			span := n.ArgSpans[0]
			// The lexeme, quotes included, is what the span covers; the rewrite
			// keeps whatever quoting was there by replacing only the path part.
			lexeme := string(f.Source[span.Start:span.End])
			rewritten := strings.Replace(lexeme, n.Args[0], shadowPath(shadow, n.Args[0]), 1)
			if rewritten == lexeme {
				walkErr = &Failure{Code: CodeVerifyFailed,
					Message: fmt.Sprintf(
						"cannot rewrite the include at %s:%d for a pre-flight", f.Path, n.Line)}
				return
			}

			out = append(out, plan.Edit{
				File:   f.Path,
				Ref:    n.Ref,
				Span:   span,
				Before: lexeme,
				After:  rewritten,
				Reason: "preflight: redirect include into the shadow tree",
			})
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	return out, nil
}

// shadowPath mirrors an absolute path under the shadow root, so
// /etc/nginx/conf.d/a.conf becomes <shadow>/etc/nginx/conf.d/a.conf.
//
// The full path is mirrored rather than a common prefix stripped, because a
// configuration can include files from more than one tree and a stripped prefix
// would collapse two different paths onto one.
func shadowPath(shadow, real string) string {
	return filepath.Join(shadow, filepath.FromSlash(real))
}

func writeShadow(shadow, real string, body []byte) error {
	target := shadowPath(shadow, real)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("cannot build the pre-flight tree for %s: %w", real, err)
	}
	if err := os.WriteFile(target, body, 0o644); err != nil {
		return fmt.Errorf("cannot write %s into the pre-flight tree: %w", real, err)
	}
	return nil
}

func walkNodes(nodes []*config.Node, fn func(*config.Node)) {
	for _, n := range nodes {
		fn(n)
		walkNodes(n.Block, fn)
	}
}
