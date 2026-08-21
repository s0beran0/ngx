// Package plan describes a change to an nginx configuration as data, and
// decides whether that description is still true.
//
// It is the whole safety design of v0.2. Nothing here writes: a Plan is what an
// operation produces and what `apply` consumes, and separating the two is what
// makes the write mechanical -- by the time bytes are touched, every decision
// has already been made, reviewed and re-checked.
//
// The shape of an edit is deliberately the smallest thing that can express a
// change: a byte range and the bytes to put there. A Plan never carries the new
// content of a file. That is not an optimisation, it is the structural form of
// D1: formatting outside the edited range is not merely preserved, it is never
// read into the write path, so there is no code that could reformat it.
package plan

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/s0beran0/ngx/internal/config"
)

// Edit is one byte substitution in one file.
type Edit struct {
	// File is the file the bytes live in, absolute. Not the top-level
	// configuration: an edit inside an included file names that file.
	File string `json:"file"`

	// Ref is the node the edit targets, "<file>#<id>" (see config.Node.Ref).
	// It is not what locates the bytes -- Span does that -- but it is what a
	// human or an agent reads to know WHAT is being changed, and what makes a
	// plan reviewable rather than a list of offsets.
	Ref string `json:"ref"`

	// Span is the byte range being replaced, End exclusive.
	Span config.Span `json:"span"`

	// Before is the bytes currently at Span, and it is the second anchor.
	//
	// It is not redundant with Span. config_hash refuses a plan built against a
	// different configuration; Before refuses an edit whose own bytes moved,
	// which the hash can miss -- the hash covers base names and directive
	// content, so a change that preserves it while shifting an offset is
	// possible, and applying such a plan would cut in the wrong place.
	//
	// Its length must equal Span.Len(). That invariant is what makes "a plan
	// never contains a rendered file" checkable instead of aspirational.
	Before string `json:"before"`

	// After is the bytes to write in place of Before. Empty is legitimate: a
	// removal is a substitution by nothing.
	After string `json:"after"`

	// Reason is one line saying which operation produced this edit, for the
	// human reading `plan show`. It carries no meaning for apply.
	Reason string `json:"reason,omitempty"`
}

// Plan is an ordered set of edits, anchored to the configuration it was built
// against.
type Plan struct {
	// Root is the absolute path of the top-level configuration file, and it is
	// the anchor the hash cannot provide.
	//
	// config.Hash deliberately covers only the BASE NAME of each file
	// (hash.go), so that moving a configuration between directories does not
	// change its identity. That is right for reading and dangerous for
	// writing: a plan built against /etc/nginx would otherwise verify cleanly
	// against a copy under /home, whose files happen to have the same names,
	// and then write to the copy -- or worse, to the original when the caller
	// meant the copy.
	//
	// So the space anchor is explicit and exact. The design plan left this
	// open with "the conservative answer is no"; this is that answer,
	// implemented.
	Root string `json:"root"`

	// ConfigHash is config.Tree.Hash as it was when the plan was built. It
	// anchors the plan in TIME, the way D3 anchors an ID.
	ConfigHash string `json:"config_hash"`

	// Edits in the order they were produced. Apply does not follow this order
	// when writing -- it goes from the highest offset down, so earlier offsets
	// stay valid -- but the order here is what a reviewer reads.
	Edits []Edit `json:"edits"`

	// Creates are whole files that do not exist yet, and Deletes are whole
	// files that should stop existing.
	//
	// They are separate fields rather than a kind of Edit, and the reason is the
	// invariant that makes an Edit safe: Before must be exactly the bytes at its
	// span, which is what stops a plan from carrying a rendered file. A new file
	// IS its content -- there is nothing to substitute into -- so calling it an
	// edit would mean either breaking that invariant or lying about a span.
	//
	// Keeping them apart also keeps the difference visible to whoever reviews a
	// plan. "This changes 12 bytes of an existing file" and "this creates a file
	// nginx will start loading" are not the same risk, and a format that made
	// them look alike would be hiding the more dangerous one.
	Creates []Create `json:"creates,omitempty"`
	Deletes []Delete `json:"deletes,omitempty"`
}

// Create is a file that does not exist yet.
type Create struct {
	// File is the absolute path to create.
	File string `json:"file"`

	// Content is the whole file. This is the one place a plan carries file
	// content, and it is unavoidable: the file has no previous bytes to
	// substitute into.
	Content string `json:"content"`

	// Mode is the permission bits, as an octal string ("0644"), because a JSON
	// number would read as decimal to anyone inspecting the plan by eye and
	// 420 is not a recognisable file mode.
	Mode string `json:"mode"`

	Reason string `json:"reason,omitempty"`
}

// Delete is a file that should stop existing.
//
// The content is NOT carried here. Apply reads it before removing, so a
// rollback can put it back, and a plan that carried it would be a plan whose
// size grew with the file it deletes -- for no gain, since the bytes on disk are
// the only ones that can be restored anyway.
type Delete struct {
	File   string `json:"file"`
	Reason string `json:"reason,omitempty"`
}

// Refusal is why a plan cannot be applied. It carries a Code so a caller
// branches on the class rather than on the message, which is the rule the
// project learned the hard way.
type Refusal struct {
	Code    RefusalCode
	Message string
}

func (r *Refusal) Error() string { return r.Message }

// RefusalCode enumerates the ways a plan stops being true. Each maps to a
// different thing the operator has to do about it.
type RefusalCode string

const (
	// RefusalMalformed is a plan that was never valid: an inverted span, a
	// Before whose length disagrees with its span, two edits over the same
	// bytes. It is a defect in whatever produced the plan, not a change in the
	// world.
	RefusalMalformed RefusalCode = "plan_malformed"

	// RefusalWrongRoot is a plan built against a different configuration
	// tree. Applying it would write to files the caller did not read.
	RefusalWrongRoot RefusalCode = "plan_wrong_root"

	// RefusalStaleHash is a plan built against a different state of the same
	// configuration. This is the one that maps to exit 9.
	RefusalStaleHash RefusalCode = "plan_stale_hash"

	// RefusalBytesMoved is an edit whose Before no longer matches the bytes at
	// its span, even though the hash agreed. The narrower and more alarming
	// case: something changed in a way the hash did not see.
	RefusalBytesMoved RefusalCode = "plan_bytes_moved"
)

// Validate checks a plan against itself, with no access to any file.
//
// It is separate from Verify on purpose: a malformed plan is a defect in the
// producer and can be caught the moment it is built, before any IO, and long
// before somebody tries to apply it on a server.
func (p *Plan) Validate() error {
	if p.Root == "" {
		return &Refusal{RefusalMalformed, "the plan names no root configuration file"}
	}
	if p.ConfigHash == "" {
		return &Refusal{RefusalMalformed, "the plan carries no config_hash, so it is anchored to nothing"}
	}
	if len(p.Edits) == 0 && len(p.Creates) == 0 && len(p.Deletes) == 0 {
		return &Refusal{RefusalMalformed, "the plan does nothing"}
	}

	if err := p.validateFileOps(); err != nil {
		return err
	}

	for i, e := range p.Edits {
		switch {
		case e.File == "":
			return &Refusal{RefusalMalformed, fmt.Sprintf("edit %d names no file", i)}
		case e.Ref == "":
			return &Refusal{RefusalMalformed, fmt.Sprintf("edit %d names no ref, so there is no way to say what it changes", i)}
		case e.Span.Start < 0 || e.Span.End < e.Span.Start:
			return &Refusal{RefusalMalformed, fmt.Sprintf(
				"edit %d has an impossible span [%d,%d)", i, e.Span.Start, e.Span.End)}
		case len(e.Before) != e.Span.Len():
			// The invariant that keeps a plan from carrying a rendered file:
			// Before is EXACTLY the bytes at the span, so it cannot hold the
			// contents of anything larger than what is being replaced.
			return &Refusal{RefusalMalformed, fmt.Sprintf(
				"edit %d says it replaces %d byte(s) but carries %d byte(s) of before-text: "+
					"a plan describes a substitution, never the new content of a file",
				i, e.Span.Len(), len(e.Before))}
		}
	}

	return p.validateNoOverlap()
}

// validateFileOps checks the whole-file operations against each other and
// against the edits.
//
// The combinations refused here are the ones with no defined meaning, rather
// than the ones that merely look odd: creating and deleting the same path, and
// editing a file the same plan deletes. Each would leave the result depending on
// the order apply happened to pick.
func (p *Plan) validateFileOps() error {
	creating := map[string]bool{}
	for i, c := range p.Creates {
		switch {
		case c.File == "":
			return &Refusal{RefusalMalformed, fmt.Sprintf("create %d names no file", i)}
		case c.Mode == "":
			return &Refusal{RefusalMalformed, fmt.Sprintf(
				"create %d for %s carries no mode. A configuration written with the wrong "+
					"permissions is a defect even when every byte is right", i, c.File)}
		case creating[c.File]:
			return &Refusal{RefusalMalformed, fmt.Sprintf(
				"the plan creates %s twice", c.File)}
		}
		if _, err := ParseMode(c.Mode); err != nil {
			return &Refusal{RefusalMalformed, fmt.Sprintf(
				"create %d for %s has an unreadable mode %q: %v", i, c.File, c.Mode, err)}
		}
		creating[c.File] = true
	}

	deleting := map[string]bool{}
	for i, d := range p.Deletes {
		switch {
		case d.File == "":
			return &Refusal{RefusalMalformed, fmt.Sprintf("delete %d names no file", i)}
		case deleting[d.File]:
			return &Refusal{RefusalMalformed, fmt.Sprintf("the plan deletes %s twice", d.File)}
		case creating[d.File]:
			return &Refusal{RefusalMalformed, fmt.Sprintf(
				"the plan both creates and deletes %s, and which one wins would depend on "+
					"the order they were applied in", d.File)}
		}
		deleting[d.File] = true
	}

	for i, e := range p.Edits {
		if deleting[e.File] {
			return &Refusal{RefusalMalformed, fmt.Sprintf(
				"edit %d changes %s, which the same plan deletes", i, e.File)}
		}
		if creating[e.File] {
			return &Refusal{RefusalMalformed, fmt.Sprintf(
				"edit %d changes %s, which the same plan creates -- put the content in the "+
					"create instead of editing it into place", i, e.File)}
		}
	}
	return nil
}

// ParseMode reads an octal file mode as written in a plan ("0644").
//
// A string and not a number, because a JSON number reads as decimal: 420 is
// 0644 and nobody inspecting a plan by eye would recognise it.
func ParseMode(s string) (os.FileMode, error) {
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("a file mode is octal, like \"0644\": %w", err)
	}
	if n > 0o7777 {
		return 0, fmt.Errorf("%q is not a file mode", s)
	}
	return os.FileMode(n), nil
}

// validateNoOverlap refuses two edits over the same bytes of the same file.
//
// Two edits that overlap have no defined result: applying them in either order
// makes the second one's Before wrong, and applying them "at once" is not a
// thing bytes can do. Refusing at plan time turns that into a producer bug
// rather than a corrupted file.
func (p *Plan) validateNoOverlap() error {
	byFile := map[string][]Edit{}
	for _, e := range p.Edits {
		byFile[e.File] = append(byFile[e.File], e)
	}

	for file, edits := range byFile {
		sort.Slice(edits, func(i, j int) bool { return edits[i].Span.Start < edits[j].Span.Start })
		for i := 1; i < len(edits); i++ {
			if edits[i].Span.Start < edits[i-1].Span.End {
				return &Refusal{RefusalMalformed, fmt.Sprintf(
					"two edits overlap in %s: [%d,%d) and [%d,%d) -- the result would depend "+
						"on which one was applied first",
					file, edits[i-1].Span.Start, edits[i-1].Span.End,
					edits[i].Span.Start, edits[i].Span.End)}
			}
		}
	}
	return nil
}

// Verify checks a plan against the configuration as it is NOW.
//
// tree is the freshly parsed configuration. Verify does no IO of its own: the
// caller reads the world and hands it over, which keeps this decidable in a
// test without a filesystem and keeps the read path in one place.
func (p *Plan) Verify(tree *config.Tree, root string) error {
	if err := p.Validate(); err != nil {
		return err
	}

	if root != p.Root {
		return &Refusal{RefusalWrongRoot, fmt.Sprintf(
			"the plan was built against %s and this is %s. config_hash cannot tell them "+
				"apart, because it covers base names rather than paths, so the path is "+
				"checked on its own",
			p.Root, root)}
	}

	if tree.Hash != p.ConfigHash {
		return &Refusal{RefusalStaleHash, fmt.Sprintf(
			"the configuration changed since the plan was built: it was %s and is now %s. "+
				"Read it again and build a new plan",
			short(p.ConfigHash), short(tree.Hash))}
	}

	// A file the plan creates must NOT be there yet. Between building the plan
	// and applying it, somebody may have created it by hand -- and overwriting
	// a file whose contents nobody read is the one thing a create must never
	// do.
	for _, c := range p.Creates {
		if _, err := os.Stat(c.File); err == nil {
			return &Refusal{RefusalBytesMoved, fmt.Sprintf(
				"%s already exists, and this plan was built to create it. Something wrote "+
					"it since the plan was made; ngx will not overwrite a file it never read",
				c.File)}
		} else if !os.IsNotExist(err) {
			return &Refusal{RefusalBytesMoved, fmt.Sprintf(
				"cannot tell whether %s exists: %v", c.File, err)}
		}
	}

	// A file the plan deletes must still be part of the configuration that was
	// read. Deleting a path that is no longer included would remove a file
	// nobody is loading, on the strength of a plan that no longer describes the
	// world.
	sources := map[string][]byte{}
	for _, f := range tree.Files {
		sources[f.Path] = f.Source
	}
	for _, d := range p.Deletes {
		if _, ok := sources[d.File]; !ok {
			return &Refusal{RefusalBytesMoved, fmt.Sprintf(
				"%s is not part of the configuration that was read, so this plan no longer "+
					"describes it", d.File)}
		}
	}

	for i, e := range p.Edits {
		src, ok := sources[e.File]
		if !ok {
			return &Refusal{RefusalBytesMoved, fmt.Sprintf(
				"edit %d targets %s, which is not part of the configuration that was read",
				i, e.File)}
		}
		if e.Span.End > len(src) {
			return &Refusal{RefusalBytesMoved, fmt.Sprintf(
				"edit %d targets bytes [%d,%d) of %s, which is only %d byte(s) long",
				i, e.Span.Start, e.Span.End, e.File, len(src))}
		}
		if got := string(src[e.Span.Start:e.Span.End]); got != e.Before {
			return &Refusal{RefusalBytesMoved, fmt.Sprintf(
				"edit %d expected %q at [%d,%d) of %s and found %q. The hash agreed, so "+
					"something moved in a way the hash cannot see -- applying this would "+
					"cut in the wrong place",
				i, e.Before, e.Span.Start, e.Span.End, e.File, got)}
		}
	}

	return nil
}

// Files returns the files the plan touches, sorted, so a caller can report
// them or take locks in a deterministic order.
func (p *Plan) Files() []string {
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	for _, e := range p.Edits {
		add(e.File)
	}
	for _, c := range p.Creates {
		add(c.File)
	}
	for _, d := range p.Deletes {
		add(d.File)
	}
	sort.Strings(out)
	return out
}

// EditedFiles returns only the files with byte substitutions, which is what
// apply's edit pass iterates. Files() is the whole footprint, for reporting.
func (p *Plan) EditedFiles() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range p.Edits {
		if !seen[e.File] {
			seen[e.File] = true
			out = append(out, e.File)
		}
	}
	sort.Strings(out)
	return out
}

func short(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

// CodeOf returns the refusal code of an error, and whether it was a refusal at
// all. Callers branch on this rather than on the message, which is the rule
// CLAUDE.md records with the defect that bought it: a --sudo hint that keyed off
// a word in the message text, and vanished the day the repository was
// translated.
func CodeOf(err error) (RefusalCode, bool) {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code, true
	}
	return "", false
}

// Describe renders a plan for a human, one line per edit. It is what
// `plan show` prints on a terminal, and it exists here rather than in the CLI
// because the shortening rules are a property of the data.
func (p *Plan) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d edit(s), %d file(s) created, %d deleted, against %s\n",
		len(p.Edits), len(p.Creates), len(p.Deletes), p.Root)
	for _, c := range p.Creates {
		fmt.Fprintf(&b, "  create %s (%s, %d bytes)\n", c.File, c.Mode, len(c.Content))
	}
	for _, d := range p.Deletes {
		fmt.Fprintf(&b, "  delete %s\n", d.File)
	}
	for _, e := range p.Edits {
		fmt.Fprintf(&b, "  %s\n    - %s\n    + %s\n", e.Ref, oneLine(e.Before), oneLine(e.After))
	}
	return b.String()
}

// oneLine makes a multi-line replacement readable in a list without hiding
// that it spans lines.
func oneLine(s string) string {
	if s == "" {
		return "(nothing)"
	}
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > 72 {
		return s[:69] + "..."
	}
	return s
}
