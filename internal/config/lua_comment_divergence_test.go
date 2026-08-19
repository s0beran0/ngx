package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/config"
)

// A comment on the same line as a Lua block comments out the block itself,
// because "#" runs to end of line. crossplane accepts the file anyway: its Lua
// hook takes over at the directive name and never sees the comment.
//
// ngx refuses, and that refusal is CORRECT -- verified against OpenResty
// 1.27.1.2 in the Lua bench, which fails the same file with `unexpected "}"`.
// This test exists so the divergence entry in the fuzz cannot be mistaken for
// a workaround: it is a case where our stricter reading matches the server and
// the dependency does not.
func TestLuaBlockCommentedOutOnTheSameLineIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(path, []byte(
		"events { worker_connections 16; }\n"+
			"http { server { listen 8080; location / {\n"+
			"set_by_lua_block #comment {return 1}\n"+
			"} } }\n"), 0o644))

	_, err := config.Parse(config.ParseOptions{Path: path})
	require.Error(t, err, "the block is inside a comment, so nginx has no block to run")

	var problems config.ParseErrors
	require.ErrorAs(t, err, &problems)
	// The exact shape the fuzz found. With an argument before the comment the
	// refusal lands in RefusalInvalidLuaBlock instead, which was already a
	// known divergence; without one it lands here, and that is the entry this
	// test locks.
	require.Equal(t, config.RefusalMissingTerminator, problems[0].Class)
	require.Truef(t, strings.HasPrefix(problems[0].Token, "{"),
		"the token is what the tokeniser found where the block belonged: %q", problems[0].Token)
}
