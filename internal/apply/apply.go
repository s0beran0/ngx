// Package apply turns a verified plan into bytes on disk, or leaves the disk
// exactly as it was.
//
// It is the only package in ngx that writes to somebody's configuration, and it
// is deliberately the dumbest one: every decision was made while the plan was
// built, and everything here is mechanical. The interesting content is the
// ORDER of the steps, because each one exists to make a specific failure
// survivable.
//
// The atomic-write sequence is not invented here. It is derived from
// internal/update/apply_unix.go, which already swaps the ngx binary the same
// way, and the three details that matter are inherited with their reasons:
//
//	temporary file in the SAME directory   rename does not cross filesystems
//	fsync BEFORE the rename                a power cut between the two can
//	                                       otherwise leave a file of the right
//	                                       size and null content -- the failure
//	                                       mode is not "old content", it is "no
//	                                       content"
//	chmod AFTER the write                  the mode requested at creation was
//	                                       already reduced by the umask
package apply

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/plan"
)

// Validate is what decides whether the written configuration is acceptable. In
// production it runs `nginx -t`; in tests it is whatever the test needs.
//
// It is injected rather than called directly because the decision "is this
// configuration valid" belongs to nginx, not to this package, and because a
// test that had to install nginx to check a rollback would not be run.
type Validate func() error

// Options is everything Run needs. Nothing here is optional: an apply with a
// missing piece would be an apply that skipped a check.
type Options struct {
	Plan *plan.Plan

	// Tree is the configuration as it was just read, and Root the path it was
	// read from. Both go to plan.Verify before anything is written.
	Tree *config.Tree
	Root string

	// Validate runs after every file has been written and decides whether the
	// result stays.
	Validate Validate

	// Privileged, when set, is used for any file this process cannot write
	// itself. Nil means no escalation: a permission error stays a permission
	// error, which is the right default -- writing to somebody's configuration
	// as root is not something to do because it happened to be possible.
	//
	// It is consulted only AFTER an unprivileged write is refused, never
	// speculatively. A file the invoking user owns is written as that user, so
	// the common case involves no sudo at all and the file keeps its owner
	// without anybody having to preserve it.
	Privileged PrivilegedWriter
}

// PrivilegedWriter is the escalation apply uses when it cannot write a file
// itself. internal/transport provides the implementation; the interface lives
// here so this package does not depend on how privilege is obtained.
type PrivilegedWriter interface {
	WriteFile(ctx context.Context, path string, data []byte, mode os.FileMode, uid, gid int) error
	Remove(ctx context.Context, path string) error
}

// Result says what happened, per file, in enough detail for an operator to act.
type Result struct {
	// Written are the files whose new content is on disk and staying.
	Written []string `json:"written"`

	// RolledBack are the files that were written and then restored, because
	// Validate refused the result.
	RolledBack []string `json:"rolled_back,omitempty"`

	// Created and Deleted are the whole-file operations that stayed.
	Created []string `json:"created,omitempty"`
	Deleted []string `json:"deleted,omitempty"`

	// NotRestored is the state nobody wants and everybody needs to be told
	// about: files that were written, then failed to be restored. The
	// configuration on disk is neither the old one nor a validated new one, and
	// no summary line can substitute for the list.
	//
	// A Result with a non-empty NotRestored is never returned alongside a nil
	// error.
	NotRestored []string `json:"not_restored,omitempty"`
}

// FailureCode enumerates how an apply fails. A caller branches on this, never
// on the message.
type FailureCode string

const (
	// CodeVerifyFailed is the plan no longer describing the world. Nothing was
	// written.
	CodeVerifyFailed FailureCode = "apply_verify_failed"

	// CodeWriteFailed is a write that did not land. Whatever had already been
	// written is rolled back.
	CodeWriteFailed FailureCode = "apply_write_failed"

	// CodeValidateFailed is nginx refusing the result. Every file is rolled
	// back, and that is the expected shape of a bad plan reaching disk.
	CodeValidateFailed FailureCode = "apply_validate_failed"

	// CodeRollbackFailed is the serious one: a write landed, Validate refused
	// it, and putting the original back also failed.
	CodeRollbackFailed FailureCode = "apply_rollback_failed"
)

// Failure carries the code, the message and the Result, because on failure the
// per-file state is the important half.
type Failure struct {
	Code    FailureCode
	Message string
	Result  *Result
	Cause   error
}

func (f *Failure) Error() string { return f.Message }
func (f *Failure) Unwrap() error { return f.Cause }

// CodeOf returns the failure code of an error, if it is one.
func CodeOf(err error) (FailureCode, bool) {
	var f *Failure
	if errors.As(err, &f) {
		return f.Code, true
	}
	return "", false
}

// Run applies the plan.
//
// The order is the design. Nothing is written until every edit has been checked
// against the bytes currently on disk, so the common failure -- a stale plan --
// costs nothing. Once writing starts, the original content of every file
// touched is held in memory, so the uncommon failure -- nginx refusing the
// result -- is reversible.
//
// The Result is ALWAYS non-nil, error or not. A caller that had to check both
// would eventually check only one, and the branch it skipped is the one where
// files are in an unknown state -- exactly the branch that must never be
// silently dropped.
func Run(opts Options) (*Result, error) {
	if opts.Plan == nil || opts.Tree == nil || opts.Validate == nil {
		return &Result{}, &Failure{Code: CodeVerifyFailed, Result: &Result{},
			Message: "apply was called without a plan, a tree or a validator"}
	}

	if err := opts.Plan.Verify(opts.Tree, opts.Root); err != nil {
		return &Result{}, &Failure{
			Code:    CodeVerifyFailed,
			Message: "the plan does not describe this configuration, so nothing was written: " + err.Error(),
			Result:  &Result{},
			Cause:   err,
		}
	}

	// The new content of every file, computed before any of it is written. A
	// substitution that fails arithmetic fails here, with the disk untouched.
	pending, err := contents(opts.Plan, opts.Tree)
	if err != nil {
		return &Result{}, &Failure{Code: CodeVerifyFailed, Result: &Result{},
			Message: err.Error(), Cause: err}
	}

	files := opts.Plan.EditedFiles()
	originals := map[string][]byte{}
	for _, f := range files {
		originals[f] = sourceOf(opts.Tree, f)
	}

	// The content of every file being deleted, read BEFORE anything is
	// touched. Without it a rollback could not put the file back, and a delete
	// that cannot be undone is not part of a transaction.
	deleted := map[string][]byte{}
	deletedMode := map[string]os.FileMode{}
	for _, d := range opts.Plan.Deletes {
		info, statErr := os.Stat(d.File)
		if statErr != nil {
			return &Result{}, &Failure{Code: CodeVerifyFailed, Result: &Result{}, Cause: statErr,
				Message: fmt.Sprintf("cannot read %s before deleting it, so the delete could "+
					"not be undone: %v", d.File, statErr)}
		}
		body, readErr := os.ReadFile(d.File)
		if readErr != nil {
			return &Result{}, &Failure{Code: CodeVerifyFailed, Result: &Result{}, Cause: readErr,
				Message: fmt.Sprintf("cannot read %s before deleting it: %v", d.File, readErr)}
		}
		deleted[d.File] = body
		deletedMode[d.File] = info.Mode().Perm()
	}

	undo := &undoLog{deleted: deleted, deletedMode: deletedMode, originals: originals,
		elevate: opts.Privileged}

	var written []string
	for _, path := range files {
		if err := writeAtomically(path, pending[path], opts.Privileged); err != nil {
			// Undo what landed. The file that failed is NOT in written, so it
			// is not restored: it was never changed, and writing to it again
			// to "restore" it would be the one write this package has no
			// reason to make.
			res := undo.run(written, nil, nil)
			res.Written = nil
			if len(res.NotRestored) > 0 {
				return res, &Failure{Code: CodeRollbackFailed, Result: res, Cause: err,
					Message: fmt.Sprintf(
						"writing %s failed and %d file(s) could not be put back: %v",
						path, len(res.NotRestored), err)}
			}
			return res, &Failure{Code: CodeWriteFailed, Result: res, Cause: err,
				Message: fmt.Sprintf("writing %s failed; every file written before it was put back: %v",
					path, err)}
		}
		written = append(written, path)
	}

	// Creates and deletes come after the edits, so a plan that both changes an
	// include and drops the file it pointed at leaves the configuration
	// loadable at every step a reader could catch it in.
	var created, removed []string
	for _, c := range opts.Plan.Creates {
		mode, modeErr := plan.ParseMode(c.Mode)
		if modeErr != nil {
			res := undo.run(written, created, removed)
			res.Written = nil
			return res, &Failure{Code: CodeWriteFailed, Result: res, Cause: modeErr,
				Message: fmt.Sprintf("the mode for %s is unreadable: %v", c.File, modeErr)}
		}
		if err := createFile(c.File, []byte(c.Content), mode, opts.Privileged); err != nil {
			res := undo.run(written, created, removed)
			res.Written = nil
			return res, &Failure{Code: CodeWriteFailed, Result: res, Cause: err,
				Message: fmt.Sprintf("creating %s failed; everything before it was undone: %v",
					c.File, err)}
		}
		created = append(created, c.File)
	}
	for _, d := range opts.Plan.Deletes {
		if err := removeFile(d.File, opts.Privileged); err != nil {
			res := undo.run(written, created, removed)
			res.Written = nil
			return res, &Failure{Code: CodeWriteFailed, Result: res, Cause: err,
				Message: fmt.Sprintf("deleting %s failed; everything before it was undone: %v",
					d.File, err)}
		}
		removed = append(removed, d.File)
	}

	if err := opts.Validate(); err != nil {
		res := undo.run(written, created, removed)
		if len(res.NotRestored) > 0 {
			return res, &Failure{Code: CodeRollbackFailed, Result: res, Cause: err,
				Message: fmt.Sprintf(
					"the configuration was refused after being written and %d file(s) could "+
						"not be put back. The configuration on disk is neither the old one nor "+
						"a valid new one: %v", len(res.NotRestored), err)}
		}
		return res, &Failure{Code: CodeValidateFailed, Result: res, Cause: err,
			Message: "the configuration was refused, and every file was put back: " + err.Error()}
	}

	return &Result{Written: written, Created: created, Deleted: removed}, nil
}

// undoLog knows how to put everything back, and it is a type rather than a
// function because there are now three kinds of change to undo and each needs
// different material: an edited file needs its old bytes, a created file needs
// removing, a deleted file needs its bytes AND its mode.
type undoLog struct {
	originals   map[string][]byte
	deleted     map[string][]byte
	deletedMode map[string]os.FileMode

	// The same escalation the writes used. A file that needed privilege to be
	// written needs it to be put back, and a rollback that could not undo what
	// the write did would be the worst kind of half-transaction.
	elevate PrivilegedWriter
}

// run undoes exactly what happened, and reports honestly what it could not.
//
// It only ever touches paths it is given, which are the ones this apply really
// changed. A file that was never touched is a file this package has no business
// writing to, even in the name of restoring it.
func (u *undoLog) run(written, created, removed []string) *Result {
	res := &Result{}

	for _, path := range written {
		if err := writeAtomically(path, u.originals[path], u.elevate); err != nil {
			res.NotRestored = append(res.NotRestored, path)
			continue
		}
		res.RolledBack = append(res.RolledBack, path)
	}

	// A created file is undone by removing it. "Already gone" counts as
	// undone: the state asked for is reached either way.
	for _, path := range created {
		if err := removeFile(path, u.elevate); err != nil && !os.IsNotExist(err) {
			res.NotRestored = append(res.NotRestored, path)
			continue
		}
		res.RolledBack = append(res.RolledBack, path)
	}

	// A deleted file is undone by writing it back with the mode it had.
	for _, path := range removed {
		if err := createFile(path, u.deleted[path], u.deletedMode[path], u.elevate); err != nil {
			res.NotRestored = append(res.NotRestored, path)
			continue
		}
		res.RolledBack = append(res.RolledBack, path)
	}

	return res
}

// createFile writes a file that does not exist, through the same temporary file
// and rename as every other write here.
//
// O_EXCL is deliberate on the temporary file only: the target is created by the
// rename, and checking the target's absence is plan.Verify's job, done before
// anything was written. Repeating it here would be a second answer to the same
// question, and the two could disagree.
func createFile(path string, data []byte, mode os.FileMode, elevate PrivilegedWriter) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil && !isPermission(err) {
		return fmt.Errorf("cannot create the directory for %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, ".ngx-apply-*")
	if err != nil {
		if isPermission(err) && elevate != nil {
			// A new file has no previous owner to preserve, so it is created
			// as root:root -- which is what every .conf in /etc/nginx is, and
			// inventing a different owner would be a guess.
			return elevate.WriteFile(context.Background(), path, data, mode, 0, 0)
		}
		return fmt.Errorf("cannot create a temporary file next to %s: %w", path, err)
	}
	tmpName := tmp.Name()
	tmp.Close()

	if err := writeAndSync(tmpName, data, mode); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		if isPermission(err) && elevate != nil {
			return elevate.WriteFile(context.Background(), path, data, mode, 0, 0)
		}
		return fmt.Errorf("cannot put %s in place: %w", path, err)
	}
	return nil
}

// removeFile deletes a file, escalating only if the unprivileged attempt is
// refused for permissions.
func removeFile(path string, elevate PrivilegedWriter) error {
	err := os.Remove(path)
	if err == nil || os.IsNotExist(err) {
		return err
	}
	if isPermission(err) && elevate != nil {
		return elevate.Remove(context.Background(), path)
	}
	return err
}

// contents computes the new bytes of every file the plan touches.
//
// Edits are applied from the HIGHEST offset down. Going the other way would
// invalidate every later offset as soon as a replacement changed length, and
// "adjust the remaining offsets as you go" is the version of this with an
// off-by-one in it.
func contents(p *plan.Plan, tree *config.Tree) (map[string][]byte, error) {
	byFile := map[string][]plan.Edit{}
	for _, e := range p.Edits {
		byFile[e.File] = append(byFile[e.File], e)
	}

	out := map[string][]byte{}
	for path, edits := range byFile {
		src := sourceOf(tree, path)
		if src == nil {
			return nil, fmt.Errorf("apply: %s is not part of the configuration that was read", path)
		}

		sort.Slice(edits, func(i, j int) bool { return edits[i].Span.Start > edits[j].Span.Start })

		// A copy, because the tree's Source is what a rollback restores and
		// what every span still points into.
		buf := make([]byte, len(src))
		copy(buf, src)

		for _, e := range edits {
			if e.Span.End > len(buf) {
				return nil, fmt.Errorf("apply: edit on %s reaches byte %d of a %d-byte file",
					path, e.Span.End, len(buf))
			}
			next := make([]byte, 0, len(buf)-e.Span.Len()+len(e.After))
			next = append(next, buf[:e.Span.Start]...)
			next = append(next, e.After...)
			next = append(next, buf[e.Span.End:]...)
			buf = next
		}
		out[path] = buf
	}
	return out, nil
}

// writeAtomically replaces the contents of path, preserving what the file was.
//
// Mode, owner and group are read from the target and applied to the temporary
// file BEFORE the rename, because after the rename there is a window in which
// the file exists with the wrong ones -- and for an nginx configuration that
// window is enough for a reload to read a file it should not have been able to,
// or to fail reading one it should.
func writeAtomically(path string, data []byte, elevate PrivilegedWriter) error {
	dir := filepath.Dir(path)

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot read the current state of %s: %w", path, err)
	}

	// The unprivileged path is tried FIRST, always, and escalation happens only
	// after it is refused. Asking for privilege because it is available would
	// mean writing somebody's configuration as root for no reason -- and a file
	// the invoking user owns keeps its owner without anybody preserving it.
	if err := writeUnprivileged(dir, path, data, info); err == nil {
		return nil
	} else if !isPermission(err) || elevate == nil {
		return err
	}

	uid, gid := ownerOf(info)
	if err := elevate.WriteFile(context.Background(), path, data, info.Mode().Perm(), uid, gid); err != nil {
		return fmt.Errorf("cannot write %s, with or without privilege: %w", path, err)
	}
	return nil
}

// isPermission asks whether the failure was about permissions, through
// errors.Is rather than by reading a message: the string differs between
// platforms and locales, and branching on it is the defect this project already
// paid for once.
func isPermission(err error) bool {
	return errors.Is(err, os.ErrPermission)
}

func writeUnprivileged(dir, path string, data []byte, info os.FileInfo) error {

	tmp, err := os.CreateTemp(dir, ".ngx-apply-*")
	if err != nil {
		return fmt.Errorf("cannot create a temporary file next to %s: %w", path, err)
	}
	tmpName := tmp.Name()
	tmp.Close()

	cleanup := func() { _ = os.Remove(tmpName) }

	if err := writeAndSync(tmpName, data, info.Mode().Perm()); err != nil {
		cleanup()
		return fmt.Errorf("cannot write the new content for %s: %w", path, err)
	}

	// Ownership is best-effort by necessity: chown needs privilege, and an
	// unprivileged apply on a file the user owns has nothing to change. It is
	// only an error when the owner actually differs and cannot be set --
	// otherwise a normal apply on your own files would fail on every platform
	// where chown is restricted.
	if err := preserveOwner(tmpName, info); err != nil {
		cleanup()
		return fmt.Errorf("cannot preserve the owner of %s: %w", path, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("cannot put the new content in place of %s: %w", path, err)
	}
	return nil
}

func writeAndSync(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	// Before the rename, not after. A rename that lands before the data does
	// leaves a file of the right size and null content, which for a
	// configuration means nginx reads nothing where it used to read a site.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// The umask already reduced the mode at creation, so it is set again here.
	return os.Chmod(path, perm)
}

func sourceOf(tree *config.Tree, path string) []byte {
	for _, f := range tree.Files {
		if f.Path == path {
			return f.Source
		}
	}
	return nil
}
