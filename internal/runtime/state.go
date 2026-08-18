package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"

	"github.com/s0beran0/ngx/internal/output"
)

// State is what can be asserted about the nginx process on the target.
//
// What is not here is as deliberate as what is. There is no field for the
// number of workers nor for the time the master loaded the configuration:
// both depend on process inspection, which diverges between Linux and darwin
// and is fragile over SSH. A field that only sometimes exists is worse than
// no field at all, and an estimated number is worse than both -- an agent
// trusts what it reads. When that data has a trustworthy source, it becomes a
// field; until then, it does not exist.
type State struct {
	// Running is a pointer because it has three states, not two: running,
	// not running, and "could not be determined" -- which comes out as a
	// missing field, never as false. Reporting false without evidence would
	// say that nginx went down.
	Running *bool `json:"running,omitempty"`

	// MasterPID is omitted when the pidfile does not exist or cannot be
	// read.
	MasterPID int `json:"master_pid,omitempty"`

	// PIDFile is the path that was consulted.
	PIDFile string `json:"pid_file,omitempty"`

	// Diagnostics explains every unavailability. DR5 requires
	// distinguishing "I could not read it" from "it does not exist", and
	// never degrading in silence: a missing field with no diagnostic would
	// be exactly the forbidden silence. Never nil.
	Diagnostics []output.Diagnostic `json:"diagnostics"`
}

// State reads the pidfile through the transport and checks whether the
// process exists.
//
// Reading the pidfile goes through the transport's Open (SFTP in the remote
// case), and the existence check uses `kill -0`, which sends no signal at
// all: it asks the system whether the process is there. It is the only
// portable way to tell a stale pidfile from a live master without depending
// on /proc.
func (r *Runtime) State(ctx context.Context, pidPath string) (*State, error) {
	s := &State{PIDFile: pidPath, Diagnostics: []output.Diagnostic{}}

	if pidPath == "" {
		s.diag(output.SeverityWarning, CodigoEstadoProcesso,
			"the pidfile path is not known, so the state of the process cannot be determined")
		return s, nil
	}

	f, err := r.tr.Open(pidPath)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// The absence of the pidfile is evidence, not an
			// assumption: nginx removes the file when it stops. That
			// is why Running becomes false here, and stays missing in
			// the other cases.
			notRunning := false
			s.Running = &notRunning
			s.diag(output.SeverityInfo, CodigoEstadoProcesso,
				fmt.Sprintf("the pidfile %s does not exist, so the master is not running", pidPath))
		case errors.Is(err, fs.ErrPermission):
			s.diag(output.SeverityWarning, CodigoPrivilegioNecessario,
				fmt.Sprintf("the pidfile %s exists but cannot be read for lack of permission on %s; "+
					"the state of the process stays unavailable until ngx runs with read access to that file",
					pidPath, r.tr.Describe()))
		default:
			s.diag(output.SeverityWarning, CodigoEstadoProcesso,
				fmt.Sprintf("the pidfile %s cannot be read on %s: %v", pidPath, r.tr.Describe(), err))
		}
		return s, nil
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		s.diag(output.SeverityWarning, CodigoEstadoProcesso,
			fmt.Sprintf("failed to read the pidfile %s on %s: %v", pidPath, r.tr.Describe(), err))
		return s, nil
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 0 {
		s.diag(output.SeverityWarning, CodigoEstadoProcesso,
			fmt.Sprintf("the pidfile %s does not contain a pid: %q", pidPath, summarize(string(content))))
		return s, nil
	}
	s.MasterPID = pid

	// kill -0 does not signal the process; it only queries. It carries no
	// sudo: asking whether a pid exists does not require privilege, and
	// escalating here would go against DR5 with no gain at all.
	_, stderr, exit, err := r.tr.Run(ctx, []string{"kill", "-0", strconv.Itoa(pid)})
	if err != nil {
		s.diag(output.SeverityWarning, CodigoEstadoProcesso,
			fmt.Sprintf("could not check whether the pid %d is alive on %s: %v",
				pid, r.tr.Describe(), err))
		return s, nil
	}

	text := string(stderr)
	switch {
	case exit == 0:
		running := true
		s.Running = &running
	case requiresPrivilege(text):
		// The process exists but belongs to another user -- the normal
		// case, because the nginx master runs as root. Saying it is not
		// running would be false; saying it is running would be guessing
		// from the message.
		//
		// With --sudo, however, the operator has ALREADY authorized
		// privilege explicitly, and DR5 requires it to be explicit, not
		// to be refused. So the second attempt happens only here, only
		// when the first one hit a permission wall, and only with the
		// flag: nothing is escalated on its own. Without the flag, the
		// field stays unavailable and said so.
		if r.sudo && r.confirmWithPrivilege(ctx, pid) {
			running := true
			s.Running = &running
			break
		}
		s.diag(output.SeverityWarning, CodigoPrivilegioNecessario,
			fmt.Sprintf("the pid %d exists but belongs to another user, and checking its state "+
				"on %s requires privilege", pid, r.tr.Describe()))
	default:
		notRunning := false
		s.Running = &notRunning
		s.diag(output.SeverityWarning, CodigoEstadoProcesso,
			fmt.Sprintf("the pidfile %s points to the pid %d, which does not exist: stale pidfile",
				pidPath, pid))
	}

	return s, nil
}

func (s *State) diag(sev output.Severity, code, message string) {
	s.Diagnostics = append(s.Diagnostics, output.Diagnostic{
		Severity: sev,
		Code:     code,
		Message:  message,
		File:     s.PIDFile,
	})
}

// confirmWithPrivilege retries the kill -0 with sudo. It returns true only
// when the answer is unambiguous: sudo unavailable, a password required or
// any other failure return false, and then the field stays unavailable
// instead of turning into a guess.
func (r *Runtime) confirmWithPrivilege(ctx context.Context, pid int) bool {
	_, _, exit, err := r.tr.Run(ctx, []string{"sudo", "-n", "kill", "-0", strconv.Itoa(pid)})
	return err == nil && exit == 0
}
