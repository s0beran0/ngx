package ops_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/apply"
	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/ops"
)

// withIncludes builds a configuration whose http block includes conf.d/*.conf,
// which is the layout every distribution ships and the one file operations have
// to work on.
func withIncludes(t *testing.T) (*config.Tree, string, string) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755))
	root := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(root, []byte(
		"events { worker_connections 16; }\nhttp {\n  include conf.d/*.conf;\n}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "conf.d", "a.conf"), []byte(
		"server {\n  listen 8080;\n  server_name a.test;\n}\n"), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)
	return tree, root, dir
}

func TestCreateFileWritesAFileNginxWillLoad(t *testing.T) {
	tree, root, dir := withIncludes(t)
	path := filepath.Join(dir, "conf.d", "b.conf")

	p, err := ops.CreateFile(tree, root, path,
		"server {\n  listen 8081;\n  server_name b.test;\n}\n", 0o644)
	require.NoError(t, err)
	require.NoError(t, p.Validate())
	require.Len(t, p.Creates, 1)
	require.Equal(t, "0644", p.Creates[0].Mode)

	_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
		Validate: func() error { return nil }})
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	// The proof that it is loaded: a fresh parse of the same root finds it.
	again, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)
	var loaded bool
	for _, f := range again.Files {
		if f.Path == path {
			loaded = true
		}
	}
	require.True(t, loaded, "the file was written but nginx would not read it")
}

// The check that matters: a path no include reaches is refused, because the
// author would believe their site was configured.
func TestCreateFileRefusesAPathNoIncludeReaches(t *testing.T) {
	tree, root, dir := withIncludes(t)

	for name, path := range map[string]string{
		"another directory": filepath.Join(dir, "elsewhere", "b.conf"),
		"wrong extension":   filepath.Join(dir, "conf.d", "b.txt"),
		"beside the root":   filepath.Join(dir, "b.conf"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ops.CreateFile(tree, root, path, "server { listen 9; }\n", 0o644)
			requireRefusal(t, err, ops.CodeNotIncluded)
			require.NoFileExists(t, path, "the refusal still wrote the file")
		})
	}
}

func TestCreateFileRefuses(t *testing.T) {
	tree, root, dir := withIncludes(t)

	t.Run("a path that already exists", func(t *testing.T) {
		_, err := ops.CreateFile(tree, root, filepath.Join(dir, "conf.d", "a.conf"),
			"server { listen 9; }\n", 0o644)
		requireRefusal(t, err, ops.CodeFileExists)
	})

	t.Run("a relative path", func(t *testing.T) {
		_, err := ops.CreateFile(tree, root, "conf.d/b.conf", "server { listen 9; }\n", 0o644)
		requireRefusal(t, err, ops.CodeInvalidArguments)
	})

	t.Run("content that does not parse", func(t *testing.T) {
		_, err := ops.CreateFile(tree, root, filepath.Join(dir, "conf.d", "b.conf"),
			"server { listen 8081;\n", 0o644)
		requireRefusal(t, err, ops.CodeInvalidArguments)
	})

	t.Run("empty content", func(t *testing.T) {
		_, err := ops.CreateFile(tree, root, filepath.Join(dir, "conf.d", "b.conf"), "   \n", 0o644)
		requireRefusal(t, err, ops.CodeInvalidArguments)
	})
}

// Deleting says what goes with the file, because a .conf is rarely one
// directive.
func TestDeleteFileSaysWhatDisappearsWithIt(t *testing.T) {
	tree, root, dir := withIncludes(t)
	path := filepath.Join(dir, "conf.d", "a.conf")

	p, err := ops.DeleteFile(tree, root, path)
	require.NoError(t, err)
	require.Len(t, p.Deletes, 1)
	require.Contains(t, p.Deletes[0].Reason, "1 server block",
		"the reason has to say what goes with the file, not just that it goes")

	_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
		Validate: func() error { return nil }})
	require.NoError(t, err)
	require.NoFileExists(t, path)
}

func TestDeleteFileRefuses(t *testing.T) {
	tree, root, dir := withIncludes(t)

	t.Run("a file nginx does not load", func(t *testing.T) {
		other := filepath.Join(dir, "unrelated.conf")
		require.NoError(t, os.WriteFile(other, []byte("# x\n"), 0o644))
		_, err := ops.DeleteFile(tree, root, other)
		requireRefusal(t, err, ops.CodeRefNotFound)
		require.FileExists(t, other, "the refusal still deleted the file")
	})

	t.Run("the top-level configuration", func(t *testing.T) {
		_, err := ops.DeleteFile(tree, root, root)
		requireRefusal(t, err, ops.CodeUnsupportedTarget)
		require.FileExists(t, root)
	})
}

// A create that nginx refuses is undone: the file is gone afterwards, not left
// behind for somebody to find.
func TestACreateNginxRefusesIsRemovedAgain(t *testing.T) {
	tree, root, dir := withIncludes(t)
	path := filepath.Join(dir, "conf.d", "b.conf")

	p, err := ops.CreateFile(tree, root, path, "server {\n  listen 8081;\n}\n", 0o644)
	require.NoError(t, err)

	res, err := apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
		Validate: func() error { return errUnacceptable }})
	require.Error(t, err)

	code, _ := apply.CodeOf(err)
	require.Equal(t, apply.CodeValidateFailed, code)
	require.Equal(t, []string{path}, res.RolledBack)
	require.NoFileExists(t, path, "a refused create left its file on disk")
}

// A delete that nginx refuses is undone: the file comes back, with its content
// and its mode.
func TestADeleteNginxRefusesIsPutBack(t *testing.T) {
	tree, root, dir := withIncludes(t)
	path := filepath.Join(dir, "conf.d", "a.conf")

	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0o640))

	p, err := ops.DeleteFile(tree, root, path)
	require.NoError(t, err)

	res, err := apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
		Validate: func() error { return errUnacceptable }})
	require.Error(t, err)
	require.Equal(t, []string{path}, res.RolledBack)

	require.FileExists(t, path, "a refused delete did not put the file back")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "the file came back with different content")

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm(),
		"the file came back with a different mode")
}

func TestFileRefusalCodesAreLiterals(t *testing.T) {
	require.Equal(t, "ops_not_included", string(ops.CodeNotIncluded))
	require.Equal(t, "ops_file_exists", string(ops.CodeFileExists))
}

var errUnacceptable = errors.New("nginx refused it")
