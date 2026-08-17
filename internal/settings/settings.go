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

	// O koanf funde slices por concatenacao, nao por substituicao. Se o
	// usuario declarou output.redact, a lista dele deve substituir a
	// default, nunca somar-se a ela — senao nao ha como remover uma regra
	// padrao. Por isso zeramos o default aqui e deixamos o Unmarshal
	// preencher a partir do que foi declarado nos arquivos.
	if k.Exists("output.redact") {
		s.Output.Redact = nil
	}

	if err := k.Unmarshal("", s); err != nil {
		return nil, fmt.Errorf("configuracao invalida: %w", err)
	}
	return s, nil
}
