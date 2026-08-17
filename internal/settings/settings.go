// Package settings carrega o arquivo de configuracao do proprio ngx.
// A v0.1 le apenas o subconjunto que seus comandos usam; chaves de versoes
// futuras sao ignoradas sem erro, para que um arquivo escrito a partir da
// spec completa funcione hoje.
package settings

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Nginx aponta para o binario e a configuracao principal.
type Nginx struct {
	Binary string `koanf:"binary"`
	Config string `koanf:"config"`
}

// Output controla formato e redacao.
type Output struct {
	Format string   `koanf:"format"`
	Redact []string `koanf:"redact"`
}

// chaveRedact e o caminho koanf de Output.Redact, usado em Load() para
// decidir quando a lista declarada substitui a default. Extraida como
// constante porque renomear a tag `koanf:"redact"` ou `koanf:"output"`
// sem atualizar este valor quebraria a substituicao em silencio, sem
// erro de compilacao.
const chaveRedact = "output.redact"

// Settings e a configuracao efetiva do ngx.
type Settings struct {
	Nginx  Nginx  `koanf:"nginx"`
	Output Output `koanf:"output"`
}

// Defaults devolve a configuracao usada quando nenhum arquivo existe. A
// redacao vem ligada: sem ela, um get pode vazar caminho de chave privada
// para dentro do contexto de um LLM rodando em API de terceiro.
func Defaults() *Settings {
	return &Settings{
		Output: Output{
			Format: "auto",
			Redact: []string{
				"ssl_certificate_key",
				"proxy_set_header Authorization",
				"auth_basic_user_file",
			},
		},
	}
}

// Load funde o arquivo global com o local, com o local vencendo chave a
// chave. Arquivo ausente nao e erro.
func Load(globalPath, localPath string) (*Settings, error) {
	k := koanf.New(".")

	for _, p := range []string{globalPath, localPath} {
		if p == "" {
			continue
		}
		if err := k.Load(file.Provider(p), yaml.Parser()); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("ao carregar %s: %w", p, err)
		}
	}

	s := Defaults()

	// O mapstructure (usado pelo koanf no Unmarshal) reaproveita o slice
	// nao-nil ja presente na struct de destino e o preenche por indice, em
	// vez de aloca-lo do zero. Isso deixaria defaults sobrando na cauda
	// sempre que a lista do usuario for menor que a default — por exemplo,
	// uma lista de 1 item por cima dos 3 defaults deixaria os ultimos 2
	// defaults intocados. Zeramos aqui para forcar substituicao total,
	// independente de como a versao fixada do mapstructure se comporta.
	//
	// A zeragem so deve acontecer quando o usuario de fato declarou uma
	// lista (mesmo vazia). Se a chave esta ausente, ou presente mas nula —
	// caso tipico de um arquivo onde a pessoa comentou todos os itens da
	// lista —, k.Get devolve nil e os defaults devem sobreviver; do
	// contrario a redacao desligaria em silencio, falhando aberta numa
	// feature de seguranca.
	if v := k.Get(chaveRedact); v != nil {
		s.Output.Redact = nil
	}

	if err := k.Unmarshal("", s); err != nil {
		return nil, fmt.Errorf("configuracao invalida: %w", err)
	}
	return s, nil
}
