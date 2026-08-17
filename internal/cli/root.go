// Package cli monta a arvore de comandos. Comandos produzem valores e erros
// tipados; a formatacao e o exit code sao responsabilidade de output.
package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/eduardoborges/ngx/internal/output"
	"github.com/eduardoborges/ngx/internal/settings"
	"github.com/spf13/cobra"
)

// Caminhos padrao do arquivo de configuracao do proprio ngx. Execute usa
// estes valores para preencher Context.GlobalSettingsPath e
// Context.LocalSettingsPath; testes de caixa-branca que precisam de
// isolamento do filesystem real sobrescrevem os campos do Context em vez de
// depender destas constantes.
const (
	GlobalSettingsPath = "/etc/ngx/ngx.yaml"
	LocalSettingsPath  = ".ngx/config.yaml"
)

// GlobalFlags espelha as flags globais da spec.
type GlobalFlags struct {
	ConfigPath   string
	JSON         bool
	Human        bool
	Quiet        bool
	NoColor      bool
	NginxBin     string
	NginxVersion string
	Timeout      time.Duration
	Profile      string
	NoRedact     bool
}

// Context carrega o que todo comando precisa.
type Context struct {
	Flags    *GlobalFlags
	Settings *settings.Settings
	Renderer *output.Renderer
	Command  string

	// GlobalSettingsPath e LocalSettingsPath sao os caminhos que preparar
	// passa para settings.Load. Execute os preenche com as constantes
	// GlobalSettingsPath/LocalSettingsPath do pacote; ficarem no Context em
	// vez de fixos no corpo de preparar e o que permite a um teste isolar o
	// carregamento das settings do filesystem real sem trocar o cwd do
	// processo inteiro.
	GlobalSettingsPath string
	LocalSettingsPath  string
}

// Execute roda o CLI e devolve o exit code. Nunca chama os.Exit: isso e
// responsabilidade de main, o que mantem o CLI inteiro testavel.
func Execute(args []string, stdout, stderr io.Writer, isTTY bool) output.ExitCode {
	ctx := &Context{
		Flags:              &GlobalFlags{},
		Renderer:           &output.Renderer{Out: stdout, IsTTY: isTTY},
		GlobalSettingsPath: GlobalSettingsPath,
		LocalSettingsPath:  LocalSettingsPath,
	}

	root := NewRoot(ctx)
	return executar(root, ctx, args, stderr)
}

// executar despacha o comando ja montado e traduz o erro em exit code. E
// separado de Execute para que um teste de caixa-branca possa injetar um
// root com um comando extra (por exemplo, um que devolva um erro tipado
// embrulhado com %w) sem duplicar a logica de normalizacao de erro e
// renderizacao do envelope.
func executar(root *cobra.Command, ctx *Context, args []string, stderr io.Writer) output.ExitCode {
	root.SetArgs(args)
	root.SetOut(stderr)
	root.SetErr(stderr)

	err := root.Execute()
	if err == nil {
		return output.ExitOK
	}

	// errors.As, nao uma type assertion direta: um comando pode devolver um
	// *output.Error embrulhado com %w para anexar contexto (padrao
	// idiomatico, ex.: fmt.Errorf("ao ler %s: %w", caminho,
	// output.InvalidConfig(...))). Uma assertion direta nao atravessa o
	// wrapping — trataria esse erro como cru e o substituiria por um Usage
	// generico, perdendo o exit code e o diagnostico originais. O cobra
	// tambem devolve erro cru (sem tipo nenhum) para flag e comando
	// invalidos; e so nesse caso que a substituicao abaixo deve acontecer.
	var e *output.Error
	if !errors.As(err, &e) {
		err = output.Usage("%s", err.Error())
	}

	renderErro(ctx, stderr, err)
	return output.CodeOf(err)
}

// renderErro desenha o envelope de erro. ctx.Renderer e sempre construido por
// Execute (ou pelo teste de caixa-branca que monta o Context), entao nunca e
// nil aqui.
func renderErro(ctx *Context, stderr io.Writer, err error) {
	env := output.New(comandoDe(ctx))
	var e *output.Error
	if errors.As(err, &e) {
		env.AddDiagnostic(e.Diag)
	} else {
		env.AddDiagnostic(output.Diagnostic{
			Severity: output.SeverityError,
			Code:     "NGX-0001",
			Message:  err.Error(),
		})
	}

	r := ctx.Renderer
	// Um erro nunca e suprimido por --quiet nem bloqueado pelo portao de
	// --no-redact: o agente precisa saber o que deu errado.
	r.Quiet = false
	r.NoRedact = false
	if renderErr := r.Render(env); renderErr != nil {
		// O cobra esta com SilenceErrors; se o proprio render do envelope de
		// erro falhar, o usuario nao pode ficar com um exit code e zero
		// bytes em qualquer stream. Isso cai no stderr como ultimo recurso.
		fmt.Fprintln(stderr, renderErr)
	}
}

// NewRoot monta o comando raiz com as flags globais.
func NewRoot(ctx *Context) *cobra.Command {
	root := &cobra.Command{
		Use:           "ngx",
		Short:         "Opera o nginx com saida estruturada e mudancas transacionais",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return preparar(ctx, cmd)
		},
	}

	f := ctx.Flags
	p := root.PersistentFlags()
	p.StringVarP(&f.ConfigPath, "config", "c", "", "configuracao principal do nginx")
	p.BoolVar(&f.JSON, "json", false, "forca saida JSON")
	p.BoolVar(&f.Human, "human", false, "forca saida legivel")
	p.BoolVarP(&f.Quiet, "quiet", "q", false, "so erros")
	p.BoolVar(&f.NoColor, "no-color", false, "desliga cores")
	p.StringVar(&f.NginxBin, "nginx-bin", "", "caminho do binario do nginx")
	p.StringVar(&f.NginxVersion, "nginx-version", "", "assume esta versao do nginx")
	p.DurationVar(&f.Timeout, "timeout", 30*time.Second, "timeout das operacoes")
	p.StringVar(&f.Profile, "profile", "", "perfil do arquivo de configuracao do ngx")
	p.BoolVar(&f.NoRedact, "no-redact", false, "mostra valores sensiveis (so em terminal)")

	root.AddCommand(newVersionCmd(ctx))
	return root
}

func preparar(ctx *Context, cmd *cobra.Command) error {
	f := ctx.Flags
	ctx.Command = cmd.Name()

	if f.JSON && f.Human {
		return output.Usage("--json e --human sao mutuamente exclusivos")
	}

	s, err := settings.Load(ctx.GlobalSettingsPath, ctx.LocalSettingsPath)
	if err != nil {
		// A causa (caminho de arquivo, erro cru do parser YAML) fica so no
		// campo Err de output.Internal, acessivel via errors.Unwrap; a
		// mensagem do diagnostico nao deve vazar detalhe interno ao agente.
		return output.Internal(err, "nao foi possivel carregar a configuracao do ngx")
	}
	ctx.Settings = s

	// O formato e validado aqui, logo depois de carregar as settings, e nao
	// so dentro de Renderer.Render: output.format vem de um YAML de
	// configuracao livre, e Render so e alcancado depois do portao de
	// --quiet. Se o valor invalido so fosse pego ali, "ngx --quiet" com um
	// format ruim suprimiria o proprio erro de uso e o usuario nao teria
	// nenhum sinal do problema.
	formato := resolverFormato(f, s)
	if err := validarFormato(formato); err != nil {
		return err
	}

	set, err := output.NewRedactSet(s.Output.Redact)
	if err != nil {
		return output.Usage("%s", err.Error())
	}

	ctx.Renderer.Format = formato
	ctx.Renderer.Redact = set
	ctx.Renderer.NoRedact = f.NoRedact
	ctx.Renderer.Quiet = f.Quiet

	return nil
}

func resolverFormato(f *GlobalFlags, s *settings.Settings) output.Format {
	switch {
	case f.JSON:
		return output.FormatJSON
	case f.Human:
		return output.FormatHuman
	default:
		return output.Format(s.Output.Format)
	}
}

// validarFormato recusa qualquer formato fora de auto/json/human. As flags
// --json/--human so produzem um desses valores por construcao; a unica
// origem possivel de um formato invalido em preparar e o output.format do
// arquivo de configuracao.
func validarFormato(formato output.Format) error {
	switch formato {
	case output.FormatAuto, output.FormatJSON, output.FormatHuman, "":
		return nil
	default:
		return output.Usage(
			"output.format invalido na configuracao: %q (esperado auto, json ou human)",
			string(formato),
		)
	}
}

func newVersionCmd(ctx *Context) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Mostra a versao do ngx",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			env := output.New("version")
			env.Data = map[string]string{"version": output.Version}
			return ctx.Renderer.Render(env)
		},
	}
}
