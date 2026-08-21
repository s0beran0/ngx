//go:build integration

package ops_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/s0beran0/ngx/internal/apply"
	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/ops"
)

// Every operation, validated by a real nginx.
//
// A unit test proves the edit is the one that was asked for. Only the binary
// says whether the RESULT is a configuration nginx would load, and that is a
// different question: a well-formed substitution can still produce something
// the server refuses, and an operation whose output only ever met a parser has
// not been tested against the thing it exists to serve.
const benchContainer = "ngx-bench-lua"

func requireBench(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available; bring the bench up to run this")
	}
	if err := exec.Command("docker", "inspect", benchContainer).Run(); err != nil {
		t.Skip("the bench is not up: run `make bench-lua-up`")
	}
}

// nginxCheck copies the configuration directory into the container and runs a
// real `openresty -t` over it.
func nginxCheck(t *testing.T, dir string) apply.Validate {
	t.Helper()
	return func() error {
		target := "/tmp/ops-" + filepath.Base(dir)
		_ = exec.Command("docker", "exec", benchContainer, "rm", "-rf", target).Run()
		if out, err := exec.Command("docker", "exec", benchContainer, "mkdir", "-p", target).CombinedOutput(); err != nil {
			t.Fatalf("mkdir in the container failed: %v\n%s", err, out)
		}
		if out, err := exec.Command("docker", "cp", dir+"/.", benchContainer+":"+target).CombinedOutput(); err != nil {
			t.Fatalf("docker cp failed: %v\n%s", err, out)
		}
		out, err := exec.Command("docker", "exec", benchContainer,
			"openresty", "-t", "-c", target+"/nginx.conf").CombinedOutput()
		if err != nil {
			return &refusedByNginx{out: string(out)}
		}
		return nil
	}
}

type refusedByNginx struct{ out string }

func (e *refusedByNginx) Error() string { return strings.TrimSpace(e.out) }

// benchSite is valid OpenResty configuration, so a failure means the CHANGE was
// wrong rather than the fixture.
const benchSite = "events { worker_connections 16; }\n" +
	"http {\n" +
	"  server {\n" +
	"    listen 8080;\n" +
	"    server_name a.test;\n" +
	"    add_header X-A \"b; c\";\n" +
	"    location / {\n" +
	"      return 200 \"ok\\n\";\n" +
	"    }\n" +
	"  }\n" +
	"}\n"

func benchWorld(t *testing.T) (*config.Tree, string, string) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755))
	root := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(root, []byte(benchSite), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)

	// The premise: the fixture is already something nginx accepts. Without
	// this, a later failure is ambiguous.
	require.NoError(t, nginxCheck(t, dir)(), "the fixture is not valid nginx configuration")

	return tree, root, dir
}

// set: a real nginx accepts the result, and the file keeps everything else.
func TestBenchSetIsAcceptedByRealNginx(t *testing.T) {
	requireBench(t)
	tree, root, dir := benchWorld(t)

	p, err := ops.Set(tree, root, refOf(t, tree, "listen"), []string{"8081"})
	require.NoError(t, err)

	res, err := apply.Run(apply.Options{Plan: p, Tree: tree, Root: root, Validate: nginxCheck(t, dir)})
	require.NoError(t, err, "real nginx refused a change it should accept")
	require.Len(t, res.Written, 1)

	got, err := os.ReadFile(root)
	require.NoError(t, err)
	require.Equal(t, strings.Replace(benchSite, "listen 8080", "listen 8081", 1), string(got))
}

// set: a quoted argument survives the round trip through nginx's own lexer,
// which is the only opinion that counts about quoting.
func TestBenchSetQuotingIsAcceptedByRealNginx(t *testing.T) {
	requireBench(t)
	tree, root, dir := benchWorld(t)

	value := "v; with { braces } and 'quotes' and #hash"
	p, err := ops.Set(tree, root, refOf(t, tree, "add_header"), []string{"X-B", value})
	require.NoError(t, err)

	_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root, Validate: nginxCheck(t, dir)})
	require.NoError(t, err, "real nginx refused the quoting ngx produced")

	again, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)

	var header *config.Node
	again.Walk(func(n *config.Node) bool {
		if header == nil && n.Directive == "add_header" {
			header = n
		}
		return true
	})
	require.NotNil(t, header)
	require.Equal(t, []string{"X-B", value}, header.Args,
		"nginx accepted the text but it does not mean what was asked for")
}

// set: a change nginx refuses is rolled back, with the binary doing the
// refusing. The argument is syntactically fine and semantically rejected --
// exactly the class a parser cannot catch.
func TestBenchSetRollsBackWhenRealNginxRefuses(t *testing.T) {
	requireBench(t)
	tree, root, dir := benchWorld(t)

	p, err := ops.Set(tree, root, refOf(t, tree, "listen"), []string{"8080", "nonsense_parameter"})
	require.NoError(t, err, "ops accepted it, which is correct: only nginx knows this is invalid")

	res, err := apply.Run(apply.Options{Plan: p, Tree: tree, Root: root, Validate: nginxCheck(t, dir)})
	require.Error(t, err, "nginx accepted an invalid listen parameter, so this test proves nothing")

	code, _ := apply.CodeOf(err)
	require.Equal(t, apply.CodeValidateFailed, code)
	require.Equal(t, []string{root}, res.RolledBack)
	require.Empty(t, res.NotRestored)

	got, err := os.ReadFile(root)
	require.NoError(t, err)
	require.Equal(t, benchSite, string(got), "the file did not come back byte for byte")
}

// add: real nginx accepts a directive ngx inserted, and the round trip through
// remove returns the file byte-identical -- both halves against the binary.
func TestBenchAddAndRemoveAreAcceptedByRealNginx(t *testing.T) {
	requireBench(t)
	tree, root, dir := benchWorld(t)

	p, err := ops.Add(tree, root, refOf(t, tree, "server"), "server_tokens", []string{"off"})
	require.NoError(t, err)

	_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root, Validate: nginxCheck(t, dir)})
	require.NoError(t, err, "real nginx refused a directive ngx inserted")

	added, err := os.ReadFile(root)
	require.NoError(t, err)
	require.Contains(t, string(added), "server_tokens off;")

	// And back. The hash anchors the plan, so removing needs a fresh read --
	// the contract working rather than a nuisance.
	tree2, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)

	var target *config.Node
	tree2.Walk(func(n *config.Node) bool {
		if target == nil && n.Directive == "server_tokens" {
			target = n
		}
		return true
	})
	require.NotNil(t, target)

	rm, err := ops.Remove(tree2, root, target.Ref)
	require.NoError(t, err)
	_, err = apply.Run(apply.Options{Plan: rm, Tree: tree2, Root: root, Validate: nginxCheck(t, dir)})
	require.NoError(t, err, "real nginx refused the file after the removal")

	back, err := os.ReadFile(root)
	require.NoError(t, err)
	require.Equal(t, benchSite, string(back),
		"add then remove did not return the file to what nginx first accepted")
}

// add: a directive real nginx refuses in that context is rolled back. The
// directive is valid syntax and wrong place -- `listen` inside `http` -- which
// is the class only the binary knows about.
func TestBenchAddRollsBackWhenRealNginxRefusesTheContext(t *testing.T) {
	requireBench(t)
	tree, root, dir := benchWorld(t)

	p, err := ops.Add(tree, root, refOf(t, tree, "http"), "listen", []string{"9999"})
	require.NoError(t, err, "ops accepted it, which is correct: context is nginx's judgement")

	res, err := apply.Run(apply.Options{Plan: p, Tree: tree, Root: root, Validate: nginxCheck(t, dir)})
	require.Error(t, err, "nginx accepted listen inside http, so this test proves nothing")

	code, _ := apply.CodeOf(err)
	require.Equal(t, apply.CodeValidateFailed, code)
	require.Equal(t, []string{root}, res.RolledBack)

	got, err := os.ReadFile(root)
	require.NoError(t, err)
	require.Equal(t, benchSite, string(got))
}

// remove: taking out a whole block leaves a configuration nginx still accepts.
// A block removal is the largest span this package produces, so it is the one
// most likely to cut in the wrong place.
func TestBenchRemovingABlockIsAcceptedByRealNginx(t *testing.T) {
	requireBench(t)
	tree, root, dir := benchWorld(t)

	var location *config.Node
	tree.Walk(func(n *config.Node) bool {
		if location == nil && n.Directive == "location" {
			location = n
		}
		return true
	})
	require.NotNil(t, location)

	p, err := ops.Remove(tree, root, location.Ref)
	require.NoError(t, err)

	_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root, Validate: nginxCheck(t, dir)})
	require.NoError(t, err, "real nginx refused the file after a block was removed")

	got, err := os.ReadFile(root)
	require.NoError(t, err)
	require.NotContains(t, string(got), "location")
	require.NotContains(t, string(got), "return 200",
		"the block's body survived its own removal")
	require.Contains(t, string(got), "listen 8080;", "the removal took more than the block")
}

// benchWithIncludes is the layout every distribution ships: a root that
// includes conf.d/*.conf. File operations only mean anything against it.
func benchWithIncludes(t *testing.T) (*config.Tree, string, string) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755))
	root := filepath.Join(dir, "nginx.conf")
	require.NoError(t, os.WriteFile(root, []byte(
		"events { worker_connections 16; }\nhttp {\n  include conf.d/*.conf;\n}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "conf.d", "a.conf"), []byte(
		"server {\n  listen 8080;\n  server_name a.test;\n}\n"), 0o644))

	tree, err := config.Parse(config.ParseOptions{Path: root})
	require.NoError(t, err)
	require.NoError(t, nginxCheck(t, dir)(), "the fixture is not valid nginx configuration")
	return tree, root, dir
}

// create: real nginx loads the new file, which is the only proof that the
// include check meant anything.
func TestBenchCreateFileIsLoadedByRealNginx(t *testing.T) {
	requireBench(t)
	tree, root, dir := benchWithIncludes(t)
	path := filepath.Join(dir, "conf.d", "b.conf")

	p, err := ops.CreateFile(tree, root, path,
		"server {\n  listen 8081;\n  server_name b.test;\n}\n", 0o644)
	require.NoError(t, err)

	res, err := apply.Run(apply.Options{Plan: p, Tree: tree, Root: root, Validate: nginxCheck(t, dir)})
	require.NoError(t, err, "real nginx refused a file ngx created")
	require.Equal(t, []string{path}, res.Created)
	require.FileExists(t, path)
}

// create: content nginx refuses in that context is undone, and the file is
// gone afterwards. `listen` at the top of an included file is valid syntax in
// the wrong place -- the class only the binary knows about.
func TestBenchCreateFileRollsBackWhenRealNginxRefuses(t *testing.T) {
	requireBench(t)
	tree, root, dir := benchWithIncludes(t)
	path := filepath.Join(dir, "conf.d", "b.conf")

	p, err := ops.CreateFile(tree, root, path, "listen 8081;\n", 0o644)
	require.NoError(t, err, "ops accepted it, which is correct: context is nginx's judgement")

	_, err = apply.Run(apply.Options{Plan: p, Tree: tree, Root: root, Validate: nginxCheck(t, dir)})
	require.Error(t, err, "nginx accepted a bare listen inside http, so this proves nothing")

	code, _ := apply.CodeOf(err)
	require.Equal(t, apply.CodeValidateFailed, code)
	require.NoFileExists(t, path, "a refused create left its file on disk")

	// And the configuration still loads, which is the point of the rollback.
	require.NoError(t, nginxCheck(t, dir)())
}

// delete: real nginx still accepts the configuration after a file is removed,
// and a refusal puts it back with content and mode.
func TestBenchDeleteFileAndItsRollback(t *testing.T) {
	requireBench(t)

	t.Run("accepted", func(t *testing.T) {
		tree, root, dir := benchWithIncludes(t)
		path := filepath.Join(dir, "conf.d", "a.conf")

		p, err := ops.DeleteFile(tree, root, path)
		require.NoError(t, err)

		res, err := apply.Run(apply.Options{Plan: p, Tree: tree, Root: root, Validate: nginxCheck(t, dir)})
		require.NoError(t, err, "real nginx refused the configuration after a delete")
		require.Equal(t, []string{path}, res.Deleted)
		require.NoFileExists(t, path)
	})

	t.Run("refused and put back", func(t *testing.T) {
		tree, root, dir := benchWithIncludes(t)
		path := filepath.Join(dir, "conf.d", "a.conf")
		before, err := os.ReadFile(path)
		require.NoError(t, err)

		// Break the root so nginx refuses whatever happens next, which makes
		// the delete's rollback the thing under test rather than the delete.
		p, err := ops.DeleteFile(tree, root, path)
		require.NoError(t, err)

		res, err := apply.Run(apply.Options{Plan: p, Tree: tree, Root: root,
			Validate: func() error { return &refusedByNginx{out: "pretend nginx refused"} }})
		require.Error(t, err)
		require.Equal(t, []string{path}, res.RolledBack)

		require.FileExists(t, path)
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, before, after)

		// And the restored configuration is one real nginx accepts.
		require.NoError(t, nginxCheck(t, dir)())
	})
}
