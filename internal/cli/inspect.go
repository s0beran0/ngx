package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/s0beran0/ngx/internal/config"
	"github.com/s0beran0/ngx/internal/output"
	"github.com/spf13/cobra"
)

// Summary e a visao de uma linha da configuracao. Existe para o agente saber
// o tamanho do que esta olhando sem ter que contar nos.
type Summary struct {
	Files     int `json:"files"`
	Servers   int `json:"servers"`
	Locations int `json:"locations"`
	Upstreams int `json:"upstreams"`
}

// InspectData e o dump completo: arvore mais resumo.
type InspectData struct {
	Config  []*config.File `json:"config"`
	Summary Summary        `json:"summary"`
}

// Redacted devolve uma copia com os valores sensiveis substituidos. A copia e
// profunda nos nos afetados: a arvore original nunca e alterada, senao um fmt
// posterior gravaria *** no arquivo do usuario.
//
// O receiver e por valor, nao por ponteiro: Render faz "data.(Redactable)"
// sobre o que esta guardado em env.Data, e RunE guarda um InspectData por
// valor (nao *InspectData). Um receiver de ponteiro aqui faria essa asserção
// falhar em silencio -- Data sairia integro, sem erro e sem aviso, mesmo com
// regras de redacao ativas (ver o comentario do campo Redact em
// output.Renderer).
func (d InspectData) Redacted(rs output.RedactSet) any {
	if rs.Empty() {
		return d
	}

	arquivos := make([]*config.File, 0, len(d.Config))
	for _, f := range d.Config {
		arquivos = append(arquivos, &config.File{
			Path:   f.Path,
			Source: f.Source,
			Nodes:  redigirNodes(f.Nodes, rs),
		})
	}
	return InspectData{Config: arquivos, Summary: d.Summary}
}

func redigirNodes(nodes []*config.Node, rs output.RedactSet) []*config.Node {
	saida := make([]*config.Node, 0, len(nodes))
	for _, n := range nodes {
		copia := *n
		if rs.Matches(n.Directive, n.Args) {
			copia.Args = []string{output.RedactedValue}
		}
		if len(n.Block) > 0 {
			copia.Block = redigirNodes(n.Block, rs)
		}
		saida = append(saida, &copia)
	}
	return saida
}

func newInspectCmd(ctx *Context) *cobra.Command {
	var combine bool

	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Dump completo: arvore de configuracao e resumo",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			caminho := caminhoDaConfig(ctx)
			if caminho == "" {
				return output.Usage("informe a configuracao com -c ou em nginx.config")
			}

			// Open e Glob vem do transporte, nunca do os/filepath direto:
			// apontado para um host remoto, um Glob local listaria os
			// arquivos da maquina do operador e os apresentaria como
			// configuracao do servidor (DR4). No alvo local o transporte e
			// justamente os.Open e filepath.Glob, entao nada muda.
			tr := ctx.transporte()
			tree, err := config.Parse(config.ParseOptions{
				Path: caminho,
				Open: tr.Open,
				Glob: tr.Glob,
			})
			if err != nil {
				return erroDeParse(err)
			}

			if combine {
				tree, err = config.Combine(tree)
				if err != nil {
					return output.Internal(err, "%s", err.Error())
				}
			}

			env := ctx.NovoEnvelope("inspect")
			env.Data = InspectData{Config: tree.Files, Summary: resumir(tree)}
			env.Meta.ConfigHash = tree.Hash
			return ctx.Renderer.Render(env)
		},
	}

	cmd.Flags().BoolVar(&combine, "combine", false, "resolve os includes numa arvore unica")
	return cmd
}

// erroDeParse traduz a falha de config.Parse para o exit code correto.
//
// config.ParseErrors representa configuracao invalida do usuario -- erro de
// sintaxe, include para arquivo inexistente -- e e exit 3
// (output.InvalidConfig), nao exit 1 (output.Internal): quem errou foi o
// .conf, nao o proprio ngx. Qualquer outra falha (arquivo ausente, erro de
// IO) continua exit 1, porque ali a flag -c estava correta e foi o disco que
// nao correspondeu.
//
// Cada item de ParseErrors carrega File e Line proprios. Eles sao
// preservados no Diagnostic (em vez de virarem so texto dentro de Message)
// para que a saida aponte o lugar exato do problema; quando ha mais de um
// item, cada um aparece localizado na mensagem, em vez de uma unica linha
// generica.
func erroDeParse(err error) error {
	var problemas config.ParseErrors
	if !errors.As(err, &problemas) || len(problemas) == 0 {
		return output.Internal(err, "%s", err.Error())
	}

	itens := make([]string, len(problemas))
	for i, p := range problemas {
		// Sem linha conhecida (arquivo que nem abriu), o `:0` seria uma
		// referencia inventada. Campo indisponivel se omite.
		if p.Line > 0 {
			itens[i] = fmt.Sprintf("%s:%d: %s", p.File, p.Line, p.Message)
		} else {
			itens[i] = fmt.Sprintf("%s: %s", p.File, p.Message)
		}
	}

	e := output.InvalidConfig("%s", strings.Join(itens, "; "))
	e.Diag.File = problemas[0].File
	e.Diag.Line = problemas[0].Line
	e.Err = err
	return e
}

func caminhoDaConfig(ctx *Context) string {
	if ctx.Flags.ConfigPath != "" {
		return ctx.Flags.ConfigPath
	}
	if ctx.Settings != nil {
		return ctx.Settings.Nginx.Config
	}
	return ""
}

// resumir conta os blocos da arvore. So diretivas que abrem bloco (via
// HasBlock) entram na contagem: a fixture tem "server 10.0.0.1:8080;" dentro
// de um upstream, que tambem se chama "server" mas e uma diretiva simples,
// nao um bloco -- contar por nome sozinho inflaria Servers.
func resumir(t *config.Tree) Summary {
	s := Summary{Files: len(t.Files)}
	t.Walk(func(n *config.Node) bool {
		if !n.HasBlock() {
			return true
		}
		switch n.Directive {
		case "server":
			s.Servers++
		case "location":
			s.Locations++
		case "upstream":
			s.Upstreams++
		}
		return true
	})
	return s
}
