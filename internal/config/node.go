// Package config is the canonical representation of the nginx configuration:
// the semantic tree comes from nginx-go-crossplane, the byte offsets come from
// this package's tokenizer, and the two are matched by token sequence.
package config

// Span is a byte range in the source file, with End exclusive.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Len returns the size of the range in bytes.
func (s Span) Len() int { return s.End - s.Start }

// Origin records where a node came from once includes were resolved.
type Origin struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Node is a directive. Span covers the whole directive, block and closing
// delimiter included; HeadSpan covers only the name and the arguments. Having
// both is what makes editing in v0.2 a byte replacement instead of a
// re-render of the file.
type Node struct {
	Directive string   `json:"directive"`
	Args      []string `json:"args"`
	File      string   `json:"file,omitempty"`
	Line      int      `json:"line"`
	Column    int      `json:"column"`
	Span      Span     `json:"span"`
	HeadSpan  Span     `json:"head_span"`

	// HeadComments holds the spans of the comments that fall INSIDE
	// HeadSpan -- "default # x\n 0;" has a comment sitting between the name
	// and the last argument. Crossplane strips those comments out of Args
	// (parse.go:286-290), so without this field they would be invisible in
	// the tree and the v0.2 rewrite, which replaces the bytes of HeadSpan,
	// would erase a comment the user wrote. Empty for the overwhelming
	// majority of nodes, hence omitempty.
	HeadComments []Span  `json:"head_comments,omitempty"`
	ID           string  `json:"id,omitempty"`
	Comment      *string `json:"comment,omitempty"`
	Block        []*Node `json:"block,omitempty"`
	Origin       *Origin `json:"origin,omitempty"`

	// hasBlock tells "server {}" apart from "server;". The Block field
	// cannot do it: an empty block is an empty slice, indistinguishable
	// from nil once serialized.
	hasBlock bool
}

// IsComment reports whether the node stands for a comment.
//
// Directive == "#" on its own is not enough: a directive whose NAME is the
// quoted text "#" also lands here with Directive == "#", and that one is a
// real directive, with arguments and even with a block. Crossplane draws the
// distinction with !IsQuoted (parse.go:264) and only fills Comment on the two
// paths that really are comments (parse.go:264-268 and parse.go:438-444);
// Comment != nil is therefore the same test, seen from this side.
func (n *Node) IsComment() bool { return n.Directive == "#" && n.Comment != nil }

// HasBlock reports whether the node opens a block, empty ones included.
func (n *Node) HasBlock() bool { return n.hasBlock }

// File is a configuration file with its original source preserved. The source
// is what makes it possible to resolve spans back into text.
type File struct {
	Path   string  `json:"file"`
	Source []byte  `json:"-"`
	Nodes  []*Node `json:"parsed"`
}

// Tree is the complete result of a parse.
type Tree struct {
	Files []*File `json:"config"`
	Hash  string  `json:"-"`
}

// Walk traverses the tree in pre-order. If fn returns false, the children of
// that node are skipped.
func (t *Tree) Walk(fn func(*Node) bool) {
	for _, f := range t.Files {
		walkNodes(f.Nodes, fn)
	}
}

func walkNodes(nodes []*Node, fn func(*Node) bool) {
	for _, n := range nodes {
		if !fn(n) {
			continue
		}
		walkNodes(n.Block, fn)
	}
}
