// Package cli monta a arvore de comandos. Comandos produzem valores e erros
// tipados; a formatacao e o exit code sao responsabilidade de output.
package cli

import (
	"io"
	"time"

	"github.com/eduardoborges/ngx/internal/output"
	"github.com/eduardoborges/ngx/internal/settings"
	"github.com/spf13/cobra"
)

// Caminhos padrao do arquivo de configuracao do proprio ngx.
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
}

// Execute roda o CLI e devolve o exit code. Nunca chama os.Exit: isso e
// responsabilidade de main, o que mantem o CLI inteiro testavel.
func Execute(args []string, stdout, stderr io.Writer, isTTY bool) output.ExitCode {
	flags := &GlobalFlags{}
	ctx := &Context{
		Flags:    flags,
		Renderer: &output.Renderer{Out: stdout, IsTTY: isTTY},
	}

	root := NewRoot(ctx)
	root.SetArgs(args)
	root.SetOut(stderr)
	root.SetErr(stderr)

	err := root.Execute()
	if err == nil {
		return output.ExitOK
	}

	// Cobra devolve erro cru para flag e comando invalidos; tratamos como uso.
	if _, ok := err.(*output.Error); !ok {
		err = output.Usage("%s", err.Error())
	}

	renderErro(ctx, stdout, isTTY, err)
	return output.CodeOf(err)
}

func renderErro(ctx *Context, stdout io.Writer, isTTY bool, err error) {
	env := output.New(comandoDe(ctx))
	var e *output.Error
	if ok := asNgxError(err, &e); ok {
		env.AddDiagnostic(e.Diag)
	} else {
		env.AddDiagnostic(output.Diagnostic{
			Severity: output.SeverityError,
			Code:     "NGX-0001",
			Message:  err.Error(),
		})
	}

	r := ctx.Renderer
	if r == nil {
		r = &output.Renderer{Out: stdout, IsTTY: isTTY}
	}
	// Um erro nunca e suprimido por --quiet nem bloqueado pelo portao de
	// --no-redact: o agente precisa saber o que deu errado.
	r.Quiet = false
	r.NoRedact = false
	_ = r.Render(env)
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

	s, err := settings.Load(GlobalSettingsPath, LocalSettingsPath)
	if err != nil {
		return output.Internal(err, "%s", err.Error())
	}
	ctx.Settings = s

	// O formato e validado aqui, logo depois de carregar as settings, e nao
	// so dentro de Renderer.Render: output.format vem de um YAML de
	// configuracao livre, e Render so e alcancado depois do portao de
	// --quiet. Se o valor invalido so fosse pego ali, "ngx --quiet" com um
	// format ruim suprimiria o proprio erro de uso e o usuario nao teria
	// nenhum sinal do problema.
	formato := resolverFormato(f, s)
	if err := validarFormato(formato, s); err != nil {
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

	cmd.SilenceUsage = true
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

// validarFormato recusa qualquer formato fora de auto/json/human. Quando as
// flags --json/--human estao presentes o formato resolvido e sempre um
// desses valores; a unica origem possivel de um formato invalido e o
// output.format do arquivo de configuracao, por isso a mensagem nomeia a
// configuracao como origem.
func validarFormato(formato output.Format, s *settings.Settings) error {
	switch formato {
	case output.FormatAuto, output.FormatJSON, output.FormatHuman, "":
		return nil
	default:
		return output.Usage(
			"output.format invalido na configuracao: %q (esperado auto, json ou human)",
			s.Output.Format,
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
