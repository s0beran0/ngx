package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/stretchr/testify/require"
)

func parseText(t *testing.T, content string) *config.Tree {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "t.conf")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: p})
	require.NoError(t, err)
	return tree
}

// Root-level context blocks carry no index: they occur at most once.
func TestRootBlocksCarryNoIndex(t *testing.T) {
	tree := parseText(t, "events {}\nhttp {}\n")

	require.Equal(t, "e", tree.Files[0].Nodes[0].ID)
	require.Equal(t, "h", tree.Files[0].Nodes[1].ID)
}

func TestServersAreNumberedAmongThemselves(t *testing.T) {
	tree := parseText(t, `http {
  server { listen 80; }
  server { listen 443; }
}`)

	http := tree.Files[0].Nodes[0]
	require.Equal(t, "h.s0", http.Block[0].ID)
	require.Equal(t, "h.s1", http.Block[1].ID)
}

// The rule that reduces brittleness: the index counts among siblings of the
// same kind, not by absolute position. Inserting a location does not renumber
// the servers.
func TestIndexCountsAmongSiblingsOfSameKind(t *testing.T) {
	tree := parseText(t, `http {
  upstream a { server 10.0.0.1; }
  server { listen 80; }
  upstream b { server 10.0.0.2; }
  server { listen 443; }
}`)

	http := tree.Files[0].Nodes[0]
	require.Equal(t, "h.u0", http.Block[0].ID)
	require.Equal(t, "h.s0", http.Block[1].ID)
	require.Equal(t, "h.u1", http.Block[2].ID)
	require.Equal(t, "h.s1", http.Block[3].ID, "the second server is still s1")
}

func TestPlainDirectivesUsePrefixD(t *testing.T) {
	tree := parseText(t, `http {
  server {
    listen 443 ssl;
    server_name api.example.com;
    location / { proxy_pass http://a; }
  }
}`)

	server := tree.Files[0].Nodes[0].Block[0]
	require.Equal(t, "h.s0.d0", server.Block[0].ID)
	require.Equal(t, "h.s0.d1", server.Block[1].ID)
	require.Equal(t, "h.s0.l0", server.Block[2].ID, "location has an abbreviation of its own")
}

// Comments get no ID and do not count towards the index: if they did, adding
// a comment would renumber the directives around it.
func TestCommentsGetNoIDAndDoNotShiftIndices(t *testing.T) {
	tree := parseText(t, `http {
  server {
    # explica o listen
    listen 443 ssl;
    # explica o name
    server_name api.example.com;
  }
}`)

	server := tree.Files[0].Nodes[0].Block[0]

	require.Empty(t, server.Block[0].ID, "a comment has no ID")
	require.Equal(t, "h.s0.d0", server.Block[1].ID)
	require.Empty(t, server.Block[2].ID)
	require.Equal(t, "h.s0.d1", server.Block[3].ID, "the comment in between did not shift the index")
}

func TestNestedLocationsChainTheID(t *testing.T) {
	tree := parseText(t, `http {
  server {
    location /a {
      location /a/b { proxy_pass http://x; }
    }
  }
}`)

	server := tree.Files[0].Nodes[0].Block[0]
	require.Equal(t, "h.s0.l0", server.Block[0].ID)
	require.Equal(t, "h.s0.l0.l0", server.Block[0].Block[0].ID)
}

// Directives with no abbreviation in the table use the full name, which keeps
// the ID readable and avoids a collision between server and stream.
func TestDirectiveWithoutAbbreviationUsesFullName(t *testing.T) {
	tree := parseText(t, `http {
  map $a $b { default 0; }
  stream { }
}`)

	http := tree.Files[0].Nodes[0]
	require.Equal(t, "h.mp0", http.Block[0].ID)
	require.Equal(t, "h.st0", http.Block[1].ID)
}

func TestFindByIDFindsTheNode(t *testing.T) {
	tree := parseText(t, `http {
  server {
    location /api { proxy_pass http://backend; }
  }
}`)

	n := config.FindByID(tree, "h.s0.l0")

	require.NotNil(t, n)
	require.Equal(t, "location", n.Directive)
	require.Equal(t, []string{"/api"}, n.Args)
}

func TestFindByIDReturnsNilWhenNotFound(t *testing.T) {
	tree := parseText(t, "http { server { listen 80; } }")

	require.Nil(t, config.FindByID(tree, "h.s9"))
}

// IDs are unique within a file and not within a configuration. On the layout
// every distribution ships -- one file per site under conf.d/*.conf -- the
// first server of every file is "s0". This test states that collision as a
// fact rather than leaving it to be discovered by a v0.2 edit landing on the
// wrong node, and pins the refusal that keeps it from being silent.
func TestFindByIDRefusesAnIDThatSeveralFilesShare(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nginx.conf"),
		[]byte("events { worker_connections 16; }\nhttp { include conf.d/*.conf; }\n"), 0o644))
	for _, name := range []string{"a", "b", "c"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "conf.d", name+".conf"),
			[]byte("server { listen 8080; server_name "+name+".test; }\n"), 0o644))
	}

	tree, err := config.Parse(config.ParseOptions{Path: filepath.Join(dir, "nginx.conf")})
	require.NoError(t, err)

	shared := 0
	tree.Walk(func(n *config.Node) bool {
		if n.ID == "s0" {
			shared++
		}
		return true
	})
	require.Equal(t, 3, shared, "one server per file, and each one is the s0 of its own file")

	require.Nil(t, config.FindByID(tree, "s0"),
		"three nodes answer to this ID, so there is no node this ID names")

	// Combine resolves the includes into one tree, and there the IDs do
	// separate. It is not the default of any command, which is why the
	// collision above is what a caller actually meets.
	combined, err := config.Combine(tree)
	require.NoError(t, err)
	require.NotNil(t, config.FindByID(combined, "h.s0"))
	require.NotNil(t, config.FindByID(combined, "h.s1"))
	require.NotNil(t, config.FindByID(combined, "h.s2"))
}

// Ref is what names a node, and this is the case that motivated it: three
// files, three servers, one ID between them.
func TestRefNamesOneNodeWhereIDNamesThree(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nginx.conf"),
		[]byte("events { worker_connections 16; }\nhttp { include conf.d/*.conf; }\n"), 0o644))
	for _, name := range []string{"a", "b", "c"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "conf.d", name+".conf"),
			[]byte("server { listen 8080; server_name "+name+".test; }\n"), 0o644))
	}

	tree, err := config.Parse(config.ParseOptions{Path: filepath.Join(dir, "nginx.conf")})
	require.NoError(t, err)

	// Every Ref in the configuration is distinct, which is the property ID
	// does not have.
	refs := map[string]int{}
	total := 0
	tree.Walk(func(n *config.Node) bool {
		if n.Ref != "" {
			refs[n.Ref]++
			total++
		}
		return true
	})
	require.NotZero(t, total)
	require.Len(t, refs, total, "two nodes share a Ref, so it does not name a node")

	// And each one resolves to the node it describes, not to the first of
	// several.
	for _, name := range []string{"a", "b", "c"} {
		ref := filepath.Join(dir, "conf.d", name+".conf") + "#s0"
		node := config.FindByRef(tree, ref)
		require.NotNilf(t, node, "%s resolved to nothing", ref)
		require.Equal(t, "server", node.Directive)
		require.Equal(t, []string{name + ".test"}, node.Block[1].Args,
			"the ref resolved to the wrong file's server")
	}
}

// Combine renumbers ID for its own view and must leave Ref alone: a node does
// not change identity because the caller asked for the includes resolved.
func TestCombineRenumbersIDAndPreservesRef(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nginx.conf"),
		[]byte("events { worker_connections 16; }\nhttp { include conf.d/*.conf; }\n"), 0o644))
	for _, name := range []string{"a", "b"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "conf.d", name+".conf"),
			[]byte("server { listen 8080; server_name "+name+".test; }\n"), 0o644))
	}

	tree, err := config.Parse(config.ParseOptions{Path: filepath.Join(dir, "nginx.conf")})
	require.NoError(t, err)
	combined, err := config.Combine(tree)
	require.NoError(t, err)

	// In the combined view the IDs separate on their own.
	require.NotNil(t, config.FindByID(combined, "h.s0"))
	require.NotNil(t, config.FindByID(combined, "h.s1"))

	// And the same nodes are still reachable by the reference they were born
	// with, which is what lets a caller read through one view and act through
	// another.
	for _, name := range []string{"a", "b"} {
		ref := filepath.Join(dir, "conf.d", name+".conf") + "#s0"
		node := config.FindByRef(combined, ref)
		require.NotNilf(t, node, "%s did not survive Combine", ref)
		require.Equal(t, []string{name + ".test"}, node.Block[1].Args)
	}
}
