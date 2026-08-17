// Package transport abstrai de onde vem a configuracao e onde os comandos
// rodam. O resto do ngx nao sabe se opera na maquina local ou num servidor
// remoto: fala sempre com um Transport.
package transport

import (
	"context"
	"io"
)

// Transport e o acesso a um alvo — a maquina local ou um host remoto.
//
// A distincao entre codigo de saida e erro em Run e a regra central da
// interface: um comando que roda ate o fim e sai com codigo diferente de
// zero devolve esse codigo com err nil, porque isso e resultado. Erro de
// transporte e o binario nao existir, a conexao cair ou o contexto ser
// cancelado — ai err e nao nulo e o exitCode nao significa nada.
//
// Confundir os dois faz um `nginx -t` que reprova a configuracao parecer
// falha de infraestrutura.
type Transport interface {
	// Open abre um arquivo para leitura. Quem chama fecha.
	Open(path string) (io.ReadCloser, error)

	// Glob expande um padrao de caminho. Sem correspondencia devolve uma
	// lista vazia e err nil, nunca nil.
	Glob(pattern string) ([]string, error)

	// Run executa argv sem shell: argv[0] e o binario e o resto sao os
	// argumentos, ja separados.
	Run(ctx context.Context, argv []string) (stdout, stderr []byte, exitCode int, err error)

	// Close libera os recursos do transporte. Chamar duas vezes e seguro.
	Close() error

	// Describe identifica o alvo em uma linha, para o meta do envelope
	// JSON: quem consome a saida precisa saber contra o que a ferramenta
	// operou. "local" para a maquina local, "ssh://user@host:porta" para
	// um host remoto.
	Describe() string
}
