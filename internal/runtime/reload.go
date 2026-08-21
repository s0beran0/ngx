package runtime

import (
	"context"
	"strings"
)

// ReloadResult is what a reload did, and what nginx said about it.
type ReloadResult struct {
	// Tested is the result of the `nginx -t` that ran first. It is always
	// present, because a reload never happens without it.
	Tested *TestResult `json:"tested"`

	// Reloaded says whether the signal was actually sent. False with a
	// non-error return means the configuration was refused and the reload was
	// not attempted -- which is the whole point of testing first.
	Reloaded bool `json:"reloaded"`

	// Raw is the combined output of the reload itself, empty on success:
	// `nginx -s reload` says nothing when it works.
	Raw string `json:"raw,omitempty"`
}

// Reload asks the running nginx to re-read its configuration.
//
// `nginx -t` runs FIRST, always, and a failure stops the reload. That ordering
// is not a convenience -- it is the difference between an operator who is told
// their configuration is wrong and a worker process that exits on start-up.
//
// It runs even when `ngx apply` has just run one. The configuration on disk can
// change between two commands, by another operator or another tool, and the cost
// of asking again is one process; the cost of not asking is a reload against
// bytes nobody validated.
//
// It is a separate operation from apply on purpose. Applying and reloading are
// different decisions: a tool that couples them cannot express "stage this now,
// reload in the maintenance window", which is how changes to a busy server
// actually happen.
func (r *Runtime) Reload(ctx context.Context) (*ReloadResult, error) {
	tested, err := r.TestConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !tested.OK {
		// Not an error from this function's point of view: nginx answered the
		// question that was asked, and the answer was no. The caller decides
		// what exit code that deserves, which is how every other command here
		// works.
		return &ReloadResult{Tested: tested, Reloaded: false}, nil
	}

	e, err := r.run(ctx, "-s", "reload")
	if err != nil {
		return nil, err
	}

	out := strings.TrimRight(e.combinedOutput(), "\n")
	if e.exit != 0 {
		return &ReloadResult{Tested: tested, Reloaded: false, Raw: out}, nil
	}
	return &ReloadResult{Tested: tested, Reloaded: true, Raw: out}, nil
}
