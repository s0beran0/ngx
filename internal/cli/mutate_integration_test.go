//go:build integration

package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The whole pipeline, through the real binary, against a real nginx.
//
// Every layer below this has its own tests, and none of them answers the
// question an operator actually has: does `ngx set | ngx apply` change the file
// and leave nginx able to load it. A library that works and a command that does
// not is indistinguishable, from outside, from nothing at all.
const mutateBench = "ngx-bench-lua"

func requireCLIBench(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available; bring the bench up to run this")
	}
	if err := exec.Command("docker", "inspect", mutateBench).Run(); err != nil {
		t.Skip("the bench is not up: run `make bench-lua-up`")
	}
	installBinary(t)
}

// installBinary cross-builds ngx for the container and puts it there, so the
// test exercises the SHIPPED command rather than a library call dressed up as
// one.
func installBinary(t *testing.T) {
	t.Helper()

	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "ngx")

	build := exec.Command("go", "build", "-o", bin, "./cmd/ngx")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	out, err := build.CombinedOutput()
	require.NoErrorf(t, err, "cross-building ngx for the container failed:\n%s", out)

	out, err = exec.Command("docker", "cp", bin, mutateBench+":/usr/local/bin/ngx").CombinedOutput()
	require.NoErrorf(t, err, "copying ngx into the container failed:\n%s", out)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "go.mod not found")
		dir = parent
	}
}

// bench runs a shell-free command in the container and returns its output and
// exit code.
func bench(t *testing.T, args ...string) (string, int) {
	t.Helper()
	full := append([]string{"exec", mutateBench}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return string(out), code
}

// benchScript is for the setup steps, where a shell inside a disposable
// container is the simplest thing that works. Nothing ngx does goes through it.
func benchScript(t *testing.T, script string) string {
	t.Helper()
	out, _ := exec.Command("docker", "exec", mutateBench, "sh", "-c", script).CombinedOutput()
	return string(out)
}

const cliSite = `server {
    listen 8080;
    server_name a.test;
}
`

func setupSite(t *testing.T) {
	t.Helper()
	benchScript(t, `rm -rf /tmp/clisite && mkdir -p /tmp/clisite/conf.d
printf 'events { worker_connections 16; }\nhttp {\n  include conf.d/*.conf;\n}\n' > /tmp/clisite/nginx.conf
printf 'server {\n    listen 8080;\n    server_name a.test;\n}\n' > /tmp/clisite/conf.d/site.conf`)

	got := benchScript(t, "cat /tmp/clisite/conf.d/site.conf")
	require.Equal(t, cliSite, got, "the fixture is not what the test assumes")
}

// refOf asks the shipped `ngx get` for a ref, which is how a caller finds one.
func refOf(t *testing.T, directive string) string {
	t.Helper()
	out, code := bench(t, "ngx", "get", "-c", "/tmp/clisite/nginx.conf",
		"--directive", directive, "--query", ".data.matches[0].ref")
	require.Zerof(t, code, "ngx get failed:\n%s", out)
	return strings.Trim(strings.TrimSpace(out), `"`)
}

func TestCLISetThenApplyChangesTheFileAndNginxAcceptsIt(t *testing.T) {
	requireCLIBench(t)
	setupSite(t)

	ref := refOf(t, "listen")
	require.Contains(t, ref, "site.conf#", "the ref is not the one get reported: %q", ref)

	plan, code := bench(t, "ngx", "set", "-c", "/tmp/clisite/nginx.conf",
		"--ref", ref, "--value", "8081")
	require.Zerof(t, code, "ngx set failed:\n%s", plan)

	// Nothing is written by set. That is the property the two-step design
	// exists for, and it is checked rather than assumed.
	require.Equal(t, cliSite, benchScript(t, "cat /tmp/clisite/conf.d/site.conf"),
		"`ngx set` wrote to the file, which it must never do")

	benchScript(t, "cat > /tmp/plan.json <<'EOF'\n"+plan+"\nEOF")

	out, code := bench(t, "ngx", "apply", "-c", "/tmp/clisite/nginx.conf", "/tmp/plan.json")
	require.Zerof(t, code, "ngx apply failed:\n%s", out)

	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Written     []string `json:"written"`
			RolledBack  []string `json:"rolled_back"`
			NotRestored []string `json:"not_restored"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &envelope))
	require.True(t, envelope.OK)
	require.Len(t, envelope.Data.Written, 1)
	require.Empty(t, envelope.Data.RolledBack)

	// Every list is a list, never null: a consumer calling .length on null
	// breaks, which is why the project states it as a rule.
	require.NotNil(t, envelope.Data.RolledBack)
	require.NotNil(t, envelope.Data.NotRestored)

	require.Equal(t, strings.Replace(cliSite, "8080", "8081", 1),
		benchScript(t, "cat /tmp/clisite/conf.d/site.conf"))

	// And the binary agrees, which is the only opinion that decides whether
	// this was a good change.
	check, code := bench(t, "openresty", "-t", "-c", "/tmp/clisite/nginx.conf")
	require.Zerof(t, code, "nginx refused the result:\n%s", check)
}

// Applying the same plan twice: the second time the configuration has moved,
// and exit 9 is the code reserved for exactly that since v0.1.
func TestCLIApplyingAStalePlanExitsNineAndWritesNothing(t *testing.T) {
	requireCLIBench(t)
	setupSite(t)

	plan, _ := bench(t, "ngx", "set", "-c", "/tmp/clisite/nginx.conf",
		"--ref", refOf(t, "listen"), "--value", "8081")
	benchScript(t, "cat > /tmp/plan.json <<'EOF'\n"+plan+"\nEOF")

	_, code := bench(t, "ngx", "apply", "-c", "/tmp/clisite/nginx.conf", "/tmp/plan.json")
	require.Zero(t, code)
	after := benchScript(t, "cat /tmp/clisite/conf.d/site.conf")

	out, code := bench(t, "ngx", "apply", "-c", "/tmp/clisite/nginx.conf", "/tmp/plan.json")
	require.Equal(t, 9, code, "a stale plan did not exit 9:\n%s", out)
	require.Equal(t, after, benchScript(t, "cat /tmp/clisite/conf.d/site.conf"),
		"the stale apply changed the file")
}

// A change nginx refuses is rolled back, through the CLI, with the real binary
// doing the refusing. `listen 8443 ssl` without a certificate is valid syntax
// and an invalid configuration -- the class a parser cannot catch.
func TestCLIApplyRollsBackWhatNginxRefuses(t *testing.T) {
	requireCLIBench(t)
	setupSite(t)

	plan, _ := bench(t, "ngx", "set", "-c", "/tmp/clisite/nginx.conf",
		"--ref", refOf(t, "listen"), "--value", "8443", "--value", "ssl")
	benchScript(t, "cat > /tmp/plan.json <<'EOF'\n"+plan+"\nEOF")

	out, code := bench(t, "ngx", "apply", "-c", "/tmp/clisite/nginx.conf", "/tmp/plan.json")
	require.Equal(t, 3, code, "nginx accepted ssl without a certificate, so this proves nothing:\n%s", out)

	require.Equal(t, cliSite, benchScript(t, "cat /tmp/clisite/conf.d/site.conf"),
		"the file was not put back after nginx refused it")
}

// --dry-run answers "would this still apply" and writes nothing, which is the
// only way to ask without risking anything.
func TestCLIDryRunWritesNothing(t *testing.T) {
	requireCLIBench(t)
	setupSite(t)

	plan, _ := bench(t, "ngx", "set", "-c", "/tmp/clisite/nginx.conf",
		"--ref", refOf(t, "listen"), "--value", "8081")
	benchScript(t, "cat > /tmp/plan.json <<'EOF'\n"+plan+"\nEOF")

	out, code := bench(t, "ngx", "apply", "-c", "/tmp/clisite/nginx.conf",
		"--dry-run", "/tmp/plan.json")
	require.Zerof(t, code, "the dry run failed:\n%s", out)
	require.Equal(t, cliSite, benchScript(t, "cat /tmp/clisite/conf.d/site.conf"))
}

// add then rm, through the CLI, returning the file byte-identical. The oracle
// the plan named, now at the level a user meets.
func TestCLIAddThenRemoveReturnsTheFileByteForByte(t *testing.T) {
	requireCLIBench(t)
	setupSite(t)

	parent := refOf(t, "server")

	plan, code := bench(t, "ngx", "add", "-c", "/tmp/clisite/nginx.conf",
		"--parent", parent, "--directive", "server_tokens", "--value", "off")
	require.Zerof(t, code, "ngx add failed:\n%s", plan)
	benchScript(t, "cat > /tmp/plan.json <<'EOF'\n"+plan+"\nEOF")

	out, code := bench(t, "ngx", "apply", "-c", "/tmp/clisite/nginx.conf", "/tmp/plan.json")
	require.Zerof(t, code, "applying the add failed:\n%s", out)
	require.Contains(t, benchScript(t, "cat /tmp/clisite/conf.d/site.conf"), "server_tokens off;")

	plan, code = bench(t, "ngx", "rm", "-c", "/tmp/clisite/nginx.conf",
		"--ref", refOf(t, "server_tokens"))
	require.Zerof(t, code, "ngx rm failed:\n%s", plan)
	benchScript(t, "cat > /tmp/plan2.json <<'EOF'\n"+plan+"\nEOF")

	out, code = bench(t, "ngx", "apply", "-c", "/tmp/clisite/nginx.conf", "/tmp/plan2.json")
	require.Zerof(t, code, "applying the removal failed:\n%s", out)

	require.Equal(t, cliSite, benchScript(t, "cat /tmp/clisite/conf.d/site.conf"),
		"add then rm did not return the file to what it was")
}

// A refusal is a refusal at the command level too: exit 2, and the file
// untouched. The target is an `if`, whose arguments crossplane rewrites.
func TestCLISetRefusesWhatItCannotExpress(t *testing.T) {
	requireCLIBench(t)
	benchScript(t, `rm -rf /tmp/clisite && mkdir -p /tmp/clisite/conf.d
printf 'events { worker_connections 16; }\nhttp {\n  include conf.d/*.conf;\n}\n' > /tmp/clisite/nginx.conf
printf 'server {\n  location / {\n    if ($request_method = POST) { return 405; }\n  }\n}\n' > /tmp/clisite/conf.d/site.conf`)

	before := benchScript(t, "cat /tmp/clisite/conf.d/site.conf")

	out, code := bench(t, "ngx", "set", "-c", "/tmp/clisite/nginx.conf",
		"--ref", refOf(t, "if"), "--value", "$a", "--value", "=", "--value", "b")
	require.Equal(t, 2, code, "setting an `if` was not refused:\n%s", out)
	require.Equal(t, before, benchScript(t, "cat /tmp/clisite/conf.d/site.conf"))
}

// --check answers "would nginx accept this" WITHOUT touching a single file.
//
// The two include styles are both here because they are the whole difficulty.
// A shadow tree that does not rewrite absolute includes makes nginx follow them
// back to the ORIGINAL files, so the check reports on a configuration it never
// changed -- see TestValidatingACopyOfTheTreeDoesNotSeeTheChange, which keeps
// that measurement honest.
func TestCLICheckAsksNginxWithoutWriting(t *testing.T) {
	requireCLIBench(t)

	for _, style := range []struct{ name, include string }{
		{"relative include", "include conf.d/*.conf;"},
		{"absolute include", "include /tmp/clisite/conf.d/*.conf;"},
	} {
		t.Run(style.name, func(t *testing.T) {
			benchScript(t, `rm -rf /tmp/clisite && mkdir -p /tmp/clisite/conf.d
printf 'events { worker_connections 16; }\nhttp {\n  `+style.include+`\n}\n' > /tmp/clisite/nginx.conf
printf 'server {\n    listen 8080;\n    server_name a.test;\n}\n' > /tmp/clisite/conf.d/site.conf`)

			require.Equal(t, cliSite, benchScript(t, "cat /tmp/clisite/conf.d/site.conf"))

			// A change nginx refuses: valid syntax, invalid configuration.
			plan, _ := bench(t, "ngx", "set", "-c", "/tmp/clisite/nginx.conf",
				"--ref", refOf(t, "listen"), "--value", "8443", "--value", "ssl")
			benchScript(t, "cat > /tmp/bad.json <<'EOF'\n"+plan+"\nEOF")

			out, code := bench(t, "ngx", "apply", "-c", "/tmp/clisite/nginx.conf",
				"--check", "/tmp/bad.json")
			require.Equal(t, 3, code,
				"the check did not catch a change nginx refuses -- with an absolute "+
					"include that means the shadow tree was reading the originals:\n%s", out)
			require.Contains(t, out, `"accepted":false`)
			require.Contains(t, out, "ssl_certificate",
				"the reason nginx gave did not reach the caller")

			// A change nginx accepts.
			plan, _ = bench(t, "ngx", "set", "-c", "/tmp/clisite/nginx.conf",
				"--ref", refOf(t, "listen"), "--value", "8081")
			benchScript(t, "cat > /tmp/good.json <<'EOF'\n"+plan+"\nEOF")

			out, code = bench(t, "ngx", "apply", "-c", "/tmp/clisite/nginx.conf",
				"--check", "/tmp/good.json")
			require.Zerof(t, code, "the check refused a change nginx accepts:\n%s", out)
			require.Contains(t, out, `"accepted":true`)

			// The property that distinguishes this from apply: nothing was
			// written, either way.
			require.Equal(t, cliSite, benchScript(t, "cat /tmp/clisite/conf.d/site.conf"),
				"--check wrote to the configuration")
		})
	}
}

// The measurement behind the paragraph above, kept as a test so the claim is
// not folklore: with an absolute include, validating a COPY of the tree reads
// the original files.
//
// If nginx ever changes this, the test fails and the design note gets revisited
// instead of quietly outliving its truth.
func TestValidatingACopyOfTheTreeDoesNotSeeTheChange(t *testing.T) {
	requireCLIBench(t)

	benchScript(t, `rm -rf /etc/shadowtest /tmp/shadowcopy
mkdir -p /etc/shadowtest/conf.d
printf 'events { worker_connections 16; }\nhttp {\n  include /etc/shadowtest/conf.d/*.conf;\n}\n' > /etc/shadowtest/nginx.conf
printf 'server { listen 8080; }\n' > /etc/shadowtest/conf.d/a.conf
mkdir -p /tmp/shadowcopy/conf.d
cp -r /etc/shadowtest/. /tmp/shadowcopy/
printf 'server { listen 8443 ssl; }\n' > /tmp/shadowcopy/conf.d/a.conf`)

	out, _ := bench(t, "openresty", "-t", "-c", "/tmp/shadowcopy/nginx.conf")

	require.Contains(t, out, "syntax is ok",
		"nginx no longer reads the original files through an absolute include in a "+
			"copied tree -- shadow validation may now be sound, and the note in "+
			"apply.go should be revisited")
	require.NotContains(t, out, "ssl_certificate",
		"the copy's broken change was seen after all, which would make shadow "+
			"validation sound")
}

// --check refuses a remote target instead of answering wrongly.
//
// The shadow is built on the machine running ngx and nginx runs on the other
// one, so a remote pre-flight would ask a remote nginx about a local path. It
// does not fail obviously: nginx answers "no such file or directory", the
// pre-flight reads that as a refusal, and the caller is told their change would
// be rejected when nothing was checked at all.
//
// Found against a real production host, which is the only place the two
// machines were actually different.
func TestCLICheckRefusesARemoteTargetRatherThanAnsweringWrongly(t *testing.T) {
	requireCLIBench(t)
	setupSite(t)

	plan, _ := bench(t, "ngx", "set", "-c", "/tmp/clisite/nginx.conf",
		"--ref", refOf(t, "listen"), "--value", "8081")
	benchScript(t, "cat > /tmp/plan.json <<'EOF'\n"+plan+"\nEOF")

	// A host that does not resolve is enough: the refusal has to come before
	// any connection is attempted, since it is about where the shadow lives.
	out, code := bench(t, "ngx", "apply", "-c", "/tmp/clisite/nginx.conf",
		"--host", "somewhere.invalid", "--check", "/tmp/plan.json")

	require.Equal(t, 2, code, "a remote --check was not refused as a usage error:\n%s", out)
	require.NotContains(t, out, `"accepted":false`,
		"a remote --check reported a verdict it could not have reached")
	require.Contains(t, out, "on this machine while nginx is on that one",
		"the refusal does not say why")
}
