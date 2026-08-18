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
	reVersao    = regexp.MustCompile(`(?m)^nginx version:\s*(\S+)`)
	reConfigure = regexp.MustCompile(`(?m)^configure arguments:\s*(.*)$`)
	reModulo    = regexp.MustCompile(`^--with-([A-Za-z0-9_]+_module)(?:=(\S+))?$`)
)

// Detect runs `nginx -V` on the target and extracts what the binary says
// about itself.
//
// `-V` writes to stderr, not to stdout -- a detail that has already broken
// more than one integration. Here both channels are taken into account, so a
// transport that merges the channels also works.
func (r *Runtime) Detect(ctx context.Context) (*Info, error) {
	e, err := r.executar(ctx, "-V")
	if err != nil {
		return nil, err
	}

	texto := e.saida()
	m := reVersao.FindStringSubmatch(texto)
	if m == nil {
		return nil, erroSaidaNaoReconhecida(r, e, "there is no \"nginx version:\" line")
	}

	info := &Info{
		Binary:           r.binario,
		Modules:          []string{},
		DynamicAvailable: []string{},
	}

	produto, versao, achou := strings.Cut(m[1], "/")
	if achou {
		info.Version = versao
		if produto != "nginx" {
			info.Flavor = produto
		}
	} else {
		info.Version = m[1]
	}

	c := reConfigure.FindStringSubmatch(texto)
	if c == nil {
		// A `-V` with no configure arguments is uncommon but not
		// impossible (slim builds, wrappers). The version is already
		// useful; the paths simply do not come out.
		return info, nil
	}
	info.ConfigureArgs = strings.TrimSpace(c[1])

	for _, arg := range dividirArgumentos(info.ConfigureArgs) {
		if mod := reModulo.FindStringSubmatch(arg); mod != nil {
			if mod[2] == "dynamic" {
				info.DynamicAvailable = append(info.DynamicAvailable, mod[1])
			} else {
				info.Modules = append(info.Modules, mod[1])
			}
			continue
		}
		chave, valor, temValor := strings.Cut(arg, "=")
		if !temValor {
			continue
		}
		switch chave {
		case "--prefix":
			info.Prefix = valor
		case "--conf-path":
			info.MainConfig = valor
		case "--pid-path":
			info.PIDPath = valor
		case "--modules-path":
			info.ModulesPath = valor
		}
	}

	// The configure paths may be relative to the prefix -- that is how the
	// default nginx.org build declares them (conf/nginx.conf). Resolving
	// here keeps each consumer from resolving it in its own way.
	//
	// The join uses path, not filepath: the path belongs to the target, and
	// the target's separator has no relation to the system where ngx runs.
	info.MainConfig = absolutoNoAlvo(info.Prefix, info.MainConfig)
	info.PIDPath = absolutoNoAlvo(info.Prefix, info.PIDPath)
	info.ModulesPath = absolutoNoAlvo(info.Prefix, info.ModulesPath)

	return info, nil
}

func absolutoNoAlvo(prefixo, caminho string) string {
	if caminho == "" || strings.HasPrefix(caminho, "/") || prefixo == "" {
		return caminho
	}
	return path.Join(prefixo, caminho)
}

// dividirArgumentos splits the configure line into arguments while respecting
// quotes. That is necessary: --with-cc-opt='-O2 -flto=auto' is a single
// argument, and a naive split on spaces would produce junk like "-flto=auto'"
// and would make the next --with-*_module be read out of context.
func dividirArgumentos(linha string) []string {
	var args []string
	var atual strings.Builder
	var aspas rune

	descarrega := func() {
		if atual.Len() > 0 {
			args = append(args, atual.String())
			atual.Reset()
		}
	}

	for _, r := range linha {
		switch {
		case aspas != 0:
			if r == aspas {
				aspas = 0
			} else {
				atual.WriteRune(r)
			}
		case r == '\'' || r == '"':
			aspas = r
		case r == ' ' || r == '\t':
			descarrega()
		default:
			atual.WriteRune(r)
		}
	}
	descarrega()

	if args == nil {
		return []string{}
	}
	return args
}
