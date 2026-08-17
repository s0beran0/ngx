package runtime

import (
	"context"
	"path"
	"regexp"
	"strings"
)

// Info descreve o nginx encontrado no alvo, como ele mesmo se descreve em
// `nginx -V`.
//
// Todo campo alem de Binary e omitido quando o `-V` nao o informa. Um nginx
// compilado sem --pid-path, por exemplo, usa o default do build, que o ngx
// nao tem como saber a partir da saida: entao PIDPath sai do JSON em vez de
// sair com um palpite plausivel.
type Info struct {
	// Binary e o que foi executado — nome do PATH ou caminho.
	Binary string `json:"binary"`

	// Version e a versao sem o prefixo do produto: "1.20.1". Variantes que
	// nao sao nginx (openresty, por exemplo) aparecem em Flavor.
	Version string `json:"version,omitempty"`

	// Flavor e o produto declarado antes da barra em "nginx version:".
	// Omitido quando e o proprio nginx.
	Flavor string `json:"flavor,omitempty"`

	Prefix      string `json:"prefix,omitempty"`
	MainConfig  string `json:"main_config,omitempty"`
	PIDPath     string `json:"pid_path,omitempty"`
	ModulesPath string `json:"modules_path,omitempty"`

	// Modules lista os modulos compilados estaticamente, como
	// "http_ssl_module". Nunca e nil.
	//
	// Modulos construidos como dinamicos (--with-x_module=dynamic) nao
	// entram: estar disponivel no disco nao e estar carregado, e so um
	// `load_module` na arvore responde isso. Lista-los aqui faria a
	// deteccao afirmar uma capacidade que o servidor pode nao ter.
	Modules []string `json:"modules"`

	// DynamicAvailable lista os modulos que o build deixou disponiveis como
	// dinamicos. Estar aqui e diferente de estar carregado. Nunca e nil.
	DynamicAvailable []string `json:"dynamic_available"`

	// ConfigureArgs e a linha de configure crua, preservada porque nenhum
	// parser cobre todas as variantes de empacotamento e quem depura
	// precisa do original.
	ConfigureArgs string `json:"configure_args,omitempty"`
}

var (
	reVersao    = regexp.MustCompile(`(?m)^nginx version:\s*(\S+)`)
	reConfigure = regexp.MustCompile(`(?m)^configure arguments:\s*(.*)$`)
	reModulo    = regexp.MustCompile(`^--with-([A-Za-z0-9_]+_module)(?:=(\S+))?$`)
)

// Detect executa `nginx -V` no alvo e extrai o que o binario diz de si.
//
// O `-V` escreve em stderr, nao em stdout — um detalhe que ja quebrou mais de
// uma integracao. Aqui os dois canais sao considerados, entao um transporte
// que junte os canais tambem funciona.
func (r *Runtime) Detect(ctx context.Context) (*Info, error) {
	e, err := r.executar(ctx, "-V")
	if err != nil {
		return nil, err
	}

	texto := e.saida()
	m := reVersao.FindStringSubmatch(texto)
	if m == nil {
		return nil, erroSaidaNaoReconhecida(r, e, "nao ha linha \"nginx version:\"")
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
		// Um `-V` sem configure arguments e incomum mas nao impossivel
		// (builds enxutos, wrappers). A versao ja e util; os caminhos
		// simplesmente nao saem.
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

	// Os caminhos do configure podem ser relativos ao prefixo — e assim que
	// o build padrao do nginx.org os declara (conf/nginx.conf). Resolver
	// aqui evita que cada consumidor resolva de um jeito.
	//
	// A juncao usa path, nao filepath: o caminho e do alvo, e o separador do
	// alvo nao tem relacao com o sistema onde o ngx roda.
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

// dividirArgumentos separa a linha de configure em argumentos respeitando
// aspas. E preciso: --with-cc-opt='-O2 -flto=auto' e um argumento so, e um
// split ingenuo por espaco produziria lixo como "-flto=auto'" e faria o
// proximo --with-*_module ser lido fora de contexto.
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
