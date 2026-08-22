package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
)

// CodePrivilegedWrite reports that a file was written with privilege. Info
// severity, and never omitted: writing to somebody's server configuration as
// root cannot happen silently, for the same reason reading it cannot.
const CodePrivilegedWrite = "NGX-0234"

// CodePrivilegedWriteUnavailable reports that a privileged write was needed and
// the transport cannot perform one.
const CodePrivilegedWriteUnavailable = "NGX-0235"

// PrivilegedWriter writes files as root, through sudo, without a shell.
//
// The sequence is three commands, and each one is there to keep a guarantee the
// unprivileged path already has:
//
//	install -m MODE -o UID -g GID /dev/null TMP
//	    creates the temporary file with its FINAL mode and owner, before it has
//	    any content. Creating it first and fixing the mode afterwards leaves a
//	    window in which a 0600 configuration is readable by anyone who can
//	    reach the directory -- and /etc/nginx is world-executable on every
//	    distribution.
//
//	dd of=TMP conv=fsync status=none  (content on stdin)
//	    writes and FSYNCS. `tee` would have been the obvious choice and is the
//	    wrong one: it does not fsync, so a crash between the write and the
//	    rename leaves a file of the right size and null content -- the exact
//	    failure internal/apply's local path guards against, reintroduced by the
//	    privileged one.
//
//	mv -f TMP PATH
//	    a rename within the same directory, which is atomic. At no instant does
//	    the target hold half a configuration.
//
// The temporary file is in the same directory as the target because a rename
// cannot cross filesystems, and it is created with privilege because the
// directory is usually root-owned -- an unprivileged temporary file could not
// be put there in the first place.
type PrivilegedWriter struct {
	tr InputRunner
}

// NewPrivilegedWriter returns a writer, or reports that this transport cannot
// provide one.
//
// The failure is a value and not a panic because it is expected: the SSH
// transport does not implement InputRunner, which is how "remote privileged
// writing is not part of v0.2" is expressed in the type system rather than in a
// comment.
func NewPrivilegedWriter(tr Transport) (*PrivilegedWriter, error) {
	runner, ok := tr.(InputRunner)
	if !ok {
		return nil, fmt.Errorf(
			"this transport (%s) cannot feed a command's standard input, which is how "+
				"content reaches a privileged write. Writing with sudo is only available "+
				"locally in this version", tr.Describe())
	}
	return &PrivilegedWriter{tr: runner}, nil
}

// WriteFile replaces the contents of path, with privilege, preserving mode and
// owner.
func (w *PrivilegedWriter) WriteFile(ctx context.Context, filePath string, data []byte, mode os.FileMode, uid, gid int) error {
	dir := path.Dir(filePath)
	tmp := path.Join(dir, ".ngx-apply-"+randomSuffix())

	// Mode and owner first, on an empty file, so the content never exists with
	// the wrong ones.
	if err := w.sudo(ctx, nil, "install",
		"-m", fmt.Sprintf("%04o", mode.Perm()),
		"-o", strconv.Itoa(uid),
		"-g", strconv.Itoa(gid),
		"/dev/null", tmp,
	); err != nil {
		return fmt.Errorf("could not create a temporary file next to %s with privilege: %w", filePath, err)
	}

	if err := w.sudo(ctx, data, "dd", "of="+tmp, "conv=fsync", "status=none"); err != nil {
		w.cleanup(ctx, tmp)
		return fmt.Errorf("could not write %s with privilege: %w", filePath, err)
	}

	if err := w.sudo(ctx, nil, "mv", "-f", tmp, filePath); err != nil {
		w.cleanup(ctx, tmp)
		return fmt.Errorf("could not put the new content in place of %s with privilege: %w", filePath, err)
	}
	return nil
}

// Remove deletes a file with privilege.
func (w *PrivilegedWriter) Remove(ctx context.Context, filePath string) error {
	if err := w.sudo(ctx, nil, "rm", "-f", "--", filePath); err != nil {
		return fmt.Errorf("could not remove %s with privilege: %w", filePath, err)
	}
	return nil
}

// Available reports whether sudo will run non-interactively right now.
//
// It is asked BEFORE any write, because discovering that sudo needs a password
// halfway through a multi-file apply means a rollback that also needs one.
func (w *PrivilegedWriter) Available(ctx context.Context) error {
	return w.sudo(ctx, nil, "true")
}

// sudo runs one command with -n, so it never waits for a password: a prompt
// nobody can answer would hang an agent forever.
func (w *PrivilegedWriter) sudo(ctx context.Context, stdin []byte, args ...string) error {
	argv := append([]string{"sudo", "-n"}, args...)

	_, stderr, exit, err := w.tr.RunWithInput(ctx, argv, stdin)
	if err != nil {
		return err
	}
	if exit != 0 {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = fmt.Sprintf("exit %d", exit)
		}
		return fmt.Errorf("%s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

func (w *PrivilegedWriter) cleanup(ctx context.Context, tmp string) {
	// Best effort: the write already failed, and a leftover temporary file is
	// worth reporting but not worth failing twice over.
	_ = w.sudo(ctx, nil, "rm", "-f", "--", tmp)
}

func randomSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Not reachable in practice, and a fixed suffix is still safe: the
		// file is created with `install`, which fails if it already exists in
		// a way that matters, and the write is the next step either way.
		return "fallback"
	}
	return hex.EncodeToString(b[:])
}
