package config

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"path/filepath"
	"strconv"
)

// Hash returns the canonical hash of the tree.
//
// What the hash protects is the meaning, not the text: comments and spacing
// are left out, so running fmt does not invalidate the IDs the agent is
// holding. Block order, on the other hand, does count, because moving a
// server changes which node each ID refers to.
func Hash(t *Tree) string {
	h := sha256.New()
	for _, f := range t.Files {
		// Only the base name goes into the hash, not the absolute path:
		// moving the configuration to another directory does not change its
		// meaning, and the absolute path varies per environment
		// (t.TempDir() in the tests, for instance).
		escreverCampo(h, filepath.Base(f.Path))
		escreverNodes(h, f.Nodes)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func escreverNodes(h hash.Hash, nodes []*Node) {
	for _, n := range nodes {
		if n.IsComment() {
			continue
		}
		escreverCampo(h, n.Directive)
		escreverCampo(h, strconv.Itoa(len(n.Args)))
		for _, a := range n.Args {
			escreverCampo(h, a)
		}
		if n.HasBlock() {
			escreverCampo(h, "{")
			escreverNodes(h, n.Block)
			escreverCampo(h, "}")
		} else {
			escreverCampo(h, ";")
		}
	}
}

// escreverCampo uses a separator that cannot show up inside a directive, so
// that "ab c" and "a bc" never collide.
func escreverCampo(h hash.Hash, s string) {
	_, _ = h.Write([]byte(s))
	_, _ = h.Write([]byte{0})
}
