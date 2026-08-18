package runtime

import (
	"context"
	"regexp"
	"strings"

	"github.com/s0beran0/ngx/internal/output"
)

// DumpFile is one file of the effective configuration, as nginx itself
// enumerates it. Path is the path on the target.
type DumpFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Dump is the effective configuration returned by `nginx -T`: the set of
// files nginx would actually read, with the includes already resolved by it.
//
// Measured on a real production nginx, that comes to 132 files -- `-T`
// answers in a single round trip what a file-by-file read would answer in
// 132.
type Dump struct {
	// OK mirrors the exit code: `nginx -T` tests before dumping, and an
	// invalid configuration exits non-zero and with no dump at all.
	OK bool `json:"ok"`

	// ConfigFile is the top-level file, when nginx names it.
	ConfigFile string `json:"config_file,omitempty"`

	// Files is never nil.
	Files []DumpFile `json:"files"`

	// Diagnostics carries what nginx wrote to stderr during the test that
	// precedes the dump. Never nil.
	Diagnostics []output.Diagnostic `json:"diagnostics"`
}

// reFileMarker matches the header nginx emits before each file:
// "# configuration file /etc/nginx/nginx.conf:".
//
// The marker is only recognized at the start of the line and with the
// trailing colon, because a comment line inside a configuration may contain
// the same text and must not split the file in two.
var reFileMarker = regexp.MustCompile(`^# configuration file (.+):$`)

// DumpConfig runs `nginx -T` on the target and splits the output into files.
//
// This is the command that, measured on a real host, fails for an ordinary
// user and only works with sudo. Without --sudo ngx does not escalate:
// run returns the privilege error saying what the command is (DR5).
func (r *Runtime) DumpConfig(ctx context.Context) (*Dump, error) {
	e, err := r.run(ctx, "-T")
	if err != nil {
		return nil, err
	}

	d := &Dump{
		OK: e.exit == 0,
		// The dump goes to stdout; the diagnostics, to stderr. Here the
		// channels are kept apart on purpose: mixing them would put
		// diagnostic lines inside the content of a file.
		Files:       DividirDump(e.stdout),
		Diagnostics: ParseDiagnosticos(e.stderr),
		ConfigFile:  testedFile(e.stderr),
	}
	if d.ConfigFile == "" {
		d.ConfigFile = testedFile(e.stdout)
	}
	return d, nil
}

// DividirDump splits the stdout of `nginx -T` into the files that compose it.
//
// Like ParseDiagnosticos, it takes only the text: the same test holds for
// bytes coming from a local pipe or from an SSH session.
//
// Content that appears before the first marker is discarded -- it belongs to
// no file, and attributing it to the first one would be inventing provenance.
func DividirDump(text string) []DumpFile {
	files := []DumpFile{}
	if strings.TrimSpace(text) == "" {
		return files
	}

	var current *DumpFile
	var content strings.Builder

	flush := func() {
		if current != nil {
			current.Content = content.String()
			files = append(files, *current)
		}
		content.Reset()
	}

	// A trailing newline is an artifact of the dump itself, not a blank
	// line of the last file: without stripping it here, the content of the
	// last file would come out with one extra empty line compared to the
	// others.
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if m := reFileMarker.FindStringSubmatch(trimmed); m != nil {
			flush()
			current = &DumpFile{Path: m[1]}
			continue
		}
		if current == nil {
			continue
		}
		content.WriteString(trimmed)
		content.WriteString("\n")
	}
	flush()

	return files
}
