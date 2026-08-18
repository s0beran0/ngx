package runtime

import (
	"context"
	"path"
	"regexp"
	"strings"
)

// Info describes the nginx found on the target, as it describes itself in
// `nginx -V`.
//
// Every field beyond Binary is omitted when `-V` does not report it. An nginx
// compiled without --pid-path, for example, uses the build default, which ngx
// has no way of knowing from the output: so PIDPath drops out of the JSON
// instead of coming out with a plausible guess.
type Info struct {
	// Binary is what was executed -- a PATH name or a path.
	Binary string `json:"binary"`

	// Version is the version without the product prefix: "1.20.1".
	// Variants that are not nginx (openresty, for example) show up in
	// Flavor.
	Version string `json:"version,omitempty"`

	// Flavor is the product declared before the slash in "nginx version:".
	// Omitted when it is nginx itself.
	Flavor string `json:"flavor,omitempty"`

	Prefix      string `json:"prefix,omitempty"`
	MainConfig  string `json:"main_config,omitempty"`
	PIDPath     string `json:"pid_path,omitempty"`
	ModulesPath string `json:"modules_path,omitempty"`

	// Modules lists the statically compiled modules, such as
	// "http_ssl_module". Never nil.
	//
	// Modules built as dynamic (--with-x_module=dynamic) do not go in:
	// being available on disk is not being loaded, and only a `load_module`
	// in the tree answers that. Listing them here would make detection
	// claim a capability the server may not have.
	Modules []string `json:"modules"`

	// DynamicAvailable lists the modules the build left available as
	// dynamic ones. Being here is different from being loaded. Never nil.
	DynamicAvailable []string `json:"dynamic_available"`

	// ConfigureArgs is the raw configure line, preserved because no parser
	// covers every packaging variant and whoever is debugging needs the
	// original.
	ConfigureArgs string `json:"configure_args,omitempty"`
}

var (
	reVersion   = regexp.MustCompile(`(?m)^nginx version:\s*(\S+)`)
	reConfigure = regexp.MustCompile(`(?m)^configure arguments:\s*(.*)$`)
	reModule    = regexp.MustCompile(`^--with-([A-Za-z0-9_]+_module)(?:=(\S+))?$`)
)

// Detect runs `nginx -V` on the target and extracts what the binary says
// about itself.
//
// `-V` writes to stderr, not to stdout -- a detail that has already broken
// more than one integration. Here both channels are taken into account, so a
// transport that merges the channels also works.
func (r *Runtime) Detect(ctx context.Context) (*Info, error) {
	e, err := r.run(ctx, "-V")
	if err != nil {
		return nil, err
	}

	text := e.combinedOutput()
	m := reVersion.FindStringSubmatch(text)
	if m == nil {
		return nil, unrecognizedOutputError(r, e, "there is no \"nginx version:\" line")
	}

	info := &Info{
		Binary:           r.binary,
		Modules:          []string{},
		DynamicAvailable: []string{},
	}

	product, version, found := strings.Cut(m[1], "/")
	if found {
		info.Version = version
		if product != "nginx" {
			info.Flavor = product
		}
	} else {
		info.Version = m[1]
	}

	c := reConfigure.FindStringSubmatch(text)
	if c == nil {
		// A `-V` with no configure arguments is uncommon but not
		// impossible (slim builds, wrappers). The version is already
		// useful; the paths simply do not come out.
		return info, nil
	}
	info.ConfigureArgs = strings.TrimSpace(c[1])

	for _, arg := range splitArguments(info.ConfigureArgs) {
		if mod := reModule.FindStringSubmatch(arg); mod != nil {
			if mod[2] == "dynamic" {
				info.DynamicAvailable = append(info.DynamicAvailable, mod[1])
			} else {
				info.Modules = append(info.Modules, mod[1])
			}
			continue
		}
		key, value, hasValue := strings.Cut(arg, "=")
		if !hasValue {
			continue
		}
		switch key {
		case "--prefix":
			info.Prefix = value
		case "--conf-path":
			info.MainConfig = value
		case "--pid-path":
			info.PIDPath = value
		case "--modules-path":
			info.ModulesPath = value
		}
	}

	// The configure paths may be relative to the prefix -- that is how the
	// default nginx.org build declares them (conf/nginx.conf). Resolving
	// here keeps each consumer from resolving it in its own way.
	//
	// The join uses path, not filepath: the path belongs to the target, and
	// the target's separator has no relation to the system where ngx runs.
	info.MainConfig = absoluteOnTarget(info.Prefix, info.MainConfig)
	info.PIDPath = absoluteOnTarget(info.Prefix, info.PIDPath)
	info.ModulesPath = absoluteOnTarget(info.Prefix, info.ModulesPath)

	return info, nil
}

func absoluteOnTarget(prefix, p string) string {
	if p == "" || strings.HasPrefix(p, "/") || prefix == "" {
		return p
	}
	return path.Join(prefix, p)
}

// splitArguments splits the configure line into arguments while respecting
// quotes. That is necessary: --with-cc-opt='-O2 -flto=auto' is a single
// argument, and a naive split on spaces would produce junk like "-flto=auto'"
// and would make the next --with-*_module be read out of context.
func splitArguments(line string) []string {
	var args []string
	var current strings.Builder
	var quote rune

	flush := func() {
		if current.Len() > 0 {
			args = append(args, current.String())
			current.Reset()
		}
	}

	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()

	if args == nil {
		return []string{}
	}
	return args
}
