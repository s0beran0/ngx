package config_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashHasSha256Prefix(t *testing.T) {
	tree := parseText(t, "http { server { listen 80; } }")

	require.True(t, strings.HasPrefix(tree.Hash, "sha256:"))
	require.Len(t, tree.Hash, len("sha256:")+64)
}

func TestHashIsDeterministic(t *testing.T) {
	a := parseText(t, "http { server { listen 80; } }")
	b := parseText(t, "http { server { listen 80; } }")

	require.Equal(t, a.Hash, b.Hash)
}

// The hash protects the meaning, not the text: two configurations differing
// only in formatting have to produce the same hash, otherwise running fmt
// would invalidate every ID the agent is holding.
func TestDifferentFormattingProducesSameHash(t *testing.T) {
	compact := parseText(t, "http{server{listen 80;}}")
	spaced := parseText(t, `
http {
    server {
        listen 80;
    }
}
`)

	require.Equal(t, compact.Hash, spaced.Hash)
}

func TestCommentsDoNotEnterTheHash(t *testing.T) {
	without := parseText(t, "http { server { listen 80; } }")
	with := parseText(t, `# a comment
http {
  # another
  server { listen 80; }
}`)

	require.Equal(t, without.Hash, with.Hash)
}

func TestChangingAnArgumentChangesTheHash(t *testing.T) {
	a := parseText(t, "http { server { listen 80; } }")
	b := parseText(t, "http { server { listen 443; } }")

	require.NotEqual(t, a.Hash, b.Hash)
}

// Order matters: moving a server changes what the IDs mean, so it has to
// change the hash.
func TestBlockOrderChangesTheHash(t *testing.T) {
	a := parseText(t, "http { server { listen 80; } server { listen 443; } }")
	b := parseText(t, "http { server { listen 443; } server { listen 80; } }")

	require.NotEqual(t, a.Hash, b.Hash)
}

// With no separator between directive and arguments, "a b" and "ab" would collide.
func TestDifferentDirectivesDoNotCollide(t *testing.T) {
	a := parseText(t, "ab c;")
	b := parseText(t, "a bc;")

	require.NotEqual(t, a.Hash, b.Hash)
}
