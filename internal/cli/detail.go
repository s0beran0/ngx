package cli

import (
	"fmt"
	"strings"

	"github.com/s0beran0/ngx/internal/config"
)

// How much of a node goes into the output.
//
// This exists because of a measurement, not a preference. On a 61-file
// configuration, `ngx get --directive listen` costs 5537 tokens of JSON, and
// the field that answers the question -- the directive itself -- is 5.8% of it.
// The rest is byte ranges nobody reading asked for, and paths repeated once per
// node:
//
//	ref            19.3%   it is "<file>#<id>", so it repeats both
//	file           15.8%   605 occurrences for 61 files
//	span ends      31.6%   only an edit needs them
//	id              7.1%
//	line + column  10.2%
//	directive       5.8%   <- the answer
//
// Dropping what a reader does not use takes the same query from 5537 tokens to
// 1997, a 64% cut, with directive, args, ref and line still there. That is the
// difference between an agent affording to ask and an agent choosing not to.
//
// It is a FLAG and not the default, because removing fields from the envelope
// is a contract break and schema_version exists to signal exactly that. The
// case for flipping the default is recorded with these numbers in
// docs/superpowers/plans/2026-08-24-ngx-v021-token-cost.md, for a version that
// can afford to bump it.
type detailLevel string

const (
	// detailFull is every field. The default, and what an edit needs: byte
	// spans are how v0.2 replaces a directive without re-rendering the file.
	detailFull detailLevel = "full"

	// detailAnswer is what a reader uses: what the directive is, what it says,
	// where it is, and how to name it later.
	detailAnswer detailLevel = "answer"
)

func parseDetail(s string) (detailLevel, error) {
	switch detailLevel(s) {
	case detailFull, detailAnswer:
		return detailLevel(s), nil
	case "":
		return detailFull, nil
	default:
		return "", fmt.Errorf("unknown detail %q: it is %q or %q", s, detailAnswer, detailFull)
	}
}

const detailFlagHelp = "how much of each node to emit: \"full\" (everything, " +
	"including byte spans) or \"answer\" (directive, args, ref and line -- around " +
	"a third of the tokens)"

// leanNode is what detailAnswer emits: a TYPE, not the full node with fields
// zeroed.
//
// Zeroing was the first attempt and it barely helped -- 15% instead of the 42%
// the arithmetic promised -- because Span, HeadSpan and Column carry no
// omitempty, deliberately: on a full node a span of [0,0) is a real value (the
// implied terminator of a Lua block has exactly that), and absence has to stay
// distinguishable from zero. So the keys went out with zeroes in them and cost
// almost the same.
//
// Adding omitempty to those tags would have fixed the number and broken the
// meaning. A separate type fixes the number and leaves the full form alone,
// which is also the honest way to say what a level contains: it is a struct
// somebody can read, not a subtraction somebody has to infer.
type leanNode struct {
	Directive string   `json:"directive"`
	Args      []string `json:"args"`
	File      string   `json:"file,omitempty"`
	Line      int      `json:"line"`

	// Ref is the addressable identity, and the reason `answer` is still enough
	// to act on: whatever a reader decides, it can name the node to `ngx set`.
	Ref string `json:"ref,omitempty"`
	ID  string `json:"id,omitempty"`

	Comment      *string    `json:"comment,omitempty"`
	RedactedArgs []int      `json:"redacted_args,omitempty"`
	Block        []leanNode `json:"block,omitempty"`
}

// leanNodes converts a tree for output at detailAnswer.
func leanNodes(nodes []*config.Node) []leanNode {
	out := make([]leanNode, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, leanNode{
			Directive:    n.Directive,
			Args:         n.Args,
			File:         n.File,
			Line:         n.Line,
			Ref:          n.Ref,
			ID:           n.ID,
			Comment:      n.Comment,
			RedactedArgs: n.RedactedArgs,
			Block:        leanNodes(n.Block),
		})
	}
	return out
}

// commonPathPrefix returns the directory every path shares, or "" when they
// share nothing worth extracting.
//
// It exists for the table format, where the ref column repeats the whole path
// on every row. Extracting the prefix once measured 20% cheaper on a 60-row
// answer.
//
// What it does NOT do is group the rows by file, which was the obvious idea and
// is worse: measured at 30% MORE, because a group header costs more than the
// repetition it saves when there is roughly one match per file -- which is the
// shape of every "find this directive across the sites" question.
func commonPathPrefix(refs []string) string {
	if len(refs) < 2 {
		return ""
	}

	dirOf := func(ref string) string {
		path := ref
		if i := strings.LastIndex(ref, "#"); i >= 0 {
			path = ref[:i]
		}
		if i := strings.LastIndex(path, "/"); i > 0 {
			return path[:i]
		}
		return ""
	}

	prefix := dirOf(refs[0])
	for _, ref := range refs[1:] {
		prefix = sharedDir(prefix, dirOf(ref))
		if prefix == "" {
			return ""
		}
	}
	// A one-segment prefix buys nothing and costs a line of explanation.
	if strings.Count(prefix, "/") < 2 {
		return ""
	}
	return prefix
}

// sharedDir returns the longest common run of whole path segments.
//
// Whole segments, because a byte-wise prefix of "/etc/nginx-old" and
// "/etc/nginx" is "/etc/nginx", which is a directory neither of them is in.
func sharedDir(a, b string) string {
	as := strings.Split(a, "/")
	bs := strings.Split(b, "/")

	var shared []string
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] != bs[i] {
			break
		}
		shared = append(shared, as[i])
	}
	if len(shared) == 0 {
		return ""
	}
	return strings.Join(shared, "/")
}

// leanFile mirrors config.File for the same reason leanNode mirrors
// config.Node: a shape, not a subtraction.
type leanFile struct {
	Path  string     `json:"file"`
	Nodes []leanNode `json:"parsed"`
}

func leanFiles(files []*config.File) []leanFile {
	out := make([]leanFile, 0, len(files))
	for _, f := range files {
		out = append(out, leanFile{Path: f.Path, Nodes: leanNodes(f.Nodes)})
	}
	return out
}
