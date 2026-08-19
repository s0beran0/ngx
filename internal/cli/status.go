package cli

import (
	"fmt"
	"io"

	"github.com/s0beran0/ngx/internal/runtime"
	"github.com/spf13/cobra"
)

// StatusData is the state of the target's nginx: what the binary says about
// itself (`nginx -V`) and what could be determined about the process.
type StatusData struct {
	Nginx   *runtime.Info `json:"nginx"`
	Process ProcessData   `json:"process"`
}

// ProcessData is the runtime's State without the diagnostics list, which
// lives in the envelope. The fields keep the runtime's omission semantics:
// what was not determined leaves the JSON, it never goes out estimated.
type ProcessData struct {
	// Running is a pointer because it has three states: running, not running
	// and "there was no way to tell" — this last one being the absent field.
	// Never false without evidence, which would say nginx went down.
	Running *bool `json:"running,omitempty"`

	// MasterPID goes out when the pidfile can be read and holds a pid.
	MasterPID int `json:"master_pid,omitempty"`

	// PIDFile is the path that was consulted. Empty when `nginx -V` does not
	// declare --pid-path: the build default is not in the output and guessing
	// /run/nginx.pid would send the operator to look at the wrong file.
	PIDFile string `json:"pid_file,omitempty"`
}

// RenderHuman sums the state up in two lines. An absent field becomes an
// absent sentence, it does not become a zero.
func (d StatusData) RenderHuman(w io.Writer) error {
	if d.Nginx != nil {
		product := "nginx"
		if d.Nginx.Flavor != "" {
			product = d.Nginx.Flavor
		}
		version := d.Nginx.Version
		if version == "" {
			version = "(version not reported)"
		}
		if _, err := fmt.Fprintf(w, "%s %s at %s\n", product, version, d.Nginx.Binary); err != nil {
			return err
		}
		// The path is what the next command needs for -c, so it belongs in
		// the two lines a person reads. Omitted, never guessed, when the
		// build does not declare it: sending an operator to a path ngx
		// invented is worse than saying nothing.
		if d.Nginx.MainConfig != "" {
			if _, err := fmt.Fprintf(w, "config %s\n", d.Nginx.MainConfig); err != nil {
				return err
			}
		}
	}

	switch {
	case d.Process.Running == nil:
		_, err := fmt.Fprintln(w, "process state unavailable")
		return err
	case *d.Process.Running:
		if d.Process.MasterPID > 0 {
			_, err := fmt.Fprintf(w, "master %d running\n", d.Process.MasterPID)
			return err
		}
		_, err := fmt.Fprintln(w, "master running")
		return err
	default:
		_, err := fmt.Fprintln(w, "master stopped")
		return err
	}
}

// newStatusCmd registers `ngx status`: binary detection plus process state,
// on the --host target and with the privilege of --sudo.
func newStatusCmd(ctx *Context) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the target's nginx and the state of the process",
		Long: `Asks the nginx binary about itself (` + "`nginx -V`" + `) and looks at the process.

data.nginx carries the version, the flavor (openresty and friends), the paths
the build was configured with -- prefix, main_config, pid_path -- and the
modules compiled in. data.nginx.main_config is the answer to "what do I pass
to -c".

data.process.running is ABSENT when there was no way to tell, which is not the
same as false: a pidfile that the build never declared, or that this user
cannot read, produces no field and a diagnostic saying so. Do not read a
missing key as "stopped".`,
		Example: `  # what nginx is this, and is it running?
  ngx status

  # where is the configuration this nginx actually loads?
  ngx --field data.nginx.main_config status

  # just the version, for a shell variable
  ngx --field data.nginx.version status

  # was it built with the ssl module?
  ngx status --query '.data.nginx.modules[] | select(. == "http_ssl_module")'

  # a remote server, reading a root-only pidfile
  ngx --host web1 --sudo status`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			execCtx, cancel := ctx.executionContext(cmd.Context())
			defer cancel()

			rt := ctx.NewRuntime()

			info, err := rt.Detect(execCtx)
			if err != nil {
				return err
			}

			// The pidfile comes from `nginx -V` itself. When the build does
			// not declare --pid-path, the path stays empty and State returns
			// the diagnostic that explains the unavailability, instead of ngx
			// looking in a path it picked on its own.
			estado, err := rt.State(execCtx, info.PIDPath)
			if err != nil {
				return err
			}

			env := ctx.NewEnvelope("status")
			env.Meta.NginxVersion = info.Version
			env.Data = StatusData{
				Nginx: info,
				Process: ProcessData{
					Running:   estado.Running,
					MasterPID: estado.MasterPID,
					PIDFile:   estado.PIDFile,
				},
			}
			for _, d := range estado.Diagnostics {
				env.AddDiagnostic(d)
			}

			return ctx.Renderer.Render(env)
		},
	}
}
