// Package cli monta a arvore de comandos. Comandos produzem valores e erros
// tipados; a formatacao e o exit code sao responsabilidade de output.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/settings"
	"github.com/s0beran0/ngx/internal/transport"
	"github.com/s0beran0/ngx/internal/update"
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

	// Flags de acesso remoto. Sem Host, nada delas e usado e o alvo e a
	// maquina local — o comportamento de sempre.
	Host            string
	User            string
	Port            int
	Key             string
	KnownHosts      string
	InsecureHostKey bool
	Sudo            bool
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

	// Transport e o alvo das operacoes: a maquina local ou um host remoto.
	// preparar o preenche; executar o fecha, inclusive no caminho de erro.
	Transport transport.Transport

	// TransportDiags guarda o que a montagem do transporte observou (aviso
	// de --insecure-host-key, ssh-agent ausente, ~/.ssh/config ilegivel).
	// Vive no Context porque precisa alcancar o envelope tanto no sucesso
	// quanto no erro.
	TransportDiags []output.Diagnostic

	// SSHConfigPath e o ~/.ssh/config a consultar. Vazio significa o
	// caminho padrao do usuario; um teste o aponta para um arquivo de
	// fixture para nao depender do HOME de quem roda a suite.
	SSHConfigPath string

	// ConectarSSH abre a conexao remota. Vazio significa
	// transport.SSHComDiagnosticos.
	ConectarSSH ConectarSSH
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

	// O transporte fecha sempre, inclusive quando o comando falha: uma
	// conexao SSH deixada aberta sobrevive ao processo so o tempo do
	// timeout do servidor, e no teste vira goroutine vazando.
	defer func() {
		avisarFalhaAoFechar(stderr, ctx.fecharTransporte())
	}()

	err := root.Execute()
	if err == nil {
		return output.ExitOK
	}

	// O comando que ja escreveu o proprio envelope so quer o exit code. E o
	// caso de `test` com a configuracao reprovada: o resultado saiu inteiro,
	// e renderizar um envelope de erro por cima colocaria dois documentos
	// JSON no stdout.
	var jaRenderizado *erroJaRenderizado
	if errors.As(err, &jaRenderizado) {
		return output.CodeOf(err)
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
	env := ctx.NovoEnvelope(comandoDe(ctx))
	var e *output.Error
	if errors.As(err, &e) {
		env.AddDiagnostic(e.Diag)
		// Os extras contam o que aconteceu ANTES da falha -- quais arquivos
		// exigiram privilegio, por exemplo. Perde-los aqui deixaria o
		// envelope de erro menos informativo que o de sucesso, justamente
		// quando quem le mais precisa de contexto.
		for _, d := range e.Extras {
			env.AddDiagnostic(d)
		}
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
	registrarFlagsDeConexao(p, f)

	root.AddCommand(newVersionCmd(ctx))
	root.AddCommand(newInspectCmd(ctx))
	root.AddCommand(newTestCmd(ctx))
	root.AddCommand(newStatusCmd(ctx))
	return root
}

// contextoDeExecucao aplica o --timeout global a uma operacao que executa
// algo no alvo. O cancelamento e sempre devolvido e o chamador sempre o
// difere: um timeout de zero (ou negativo, digitado por engano) nao pode
// virar uma operacao sem limite nenhum pendurada numa conexao SSH, entao
// nesse caso vale o default da flag.
func (c *Context) contextoDeExecucao(pai context.Context) (context.Context, context.CancelFunc) {
	if pai == nil {
		pai = context.Background()
	}
	if c.Flags == nil || c.Flags.Timeout <= 0 {
		return context.WithCancel(pai)
	}
	return context.WithTimeout(pai, c.Flags.Timeout)
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

	// Lista de redacao vazia desliga a redacao pelo arquivo de settings, sem
	// passar pelo portao de terminal do --no-redact. E um caminho legitimo,
	// mas nao pode ser mudo: um `.ngx/config.yaml` relativo ao cwd basta
	// para um agente de IA passar a despejar segredo no pipe sem que nada na
	// saida indique que a protecao foi desligada. O aviso e o que impede
	// isso de ser invisivel para quem consome.
	if set.Empty() {
		ctx.TransportDiags = append(ctx.TransportDiags, output.Diagnostic{
			Severity: output.SeverityWarning,
			Code:     "NGX-0004",
			Message: "a redacao esta DESLIGADA: a lista output.redact do arquivo de " +
				"settings esta vazia, entao valores sensiveis saem como estao. " +
				"Isso nao passa pelo portao de terminal do --no-redact",
		})
	}

	ctx.Renderer.Format = formato
	ctx.Renderer.Redact = set
	ctx.Renderer.NoRedact = f.NoRedact
	ctx.Renderer.Quiet = f.Quiet

	// O transporte e o ultimo passo de preparar: conectar antes de validar
	// as flags cobraria um handshake SSH de quem digitou --json --human.
	return abrirTransporte(ctx, cmd)
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
			env := ctx.NovoEnvelope("version")
			dados := map[string]string{"version": output.Version}

			// A chave publica embutida sai aqui por dois motivos. Quem usa
			// consegue conferir contra a chave publicada do projeto antes de
			// confiar num `ngx update`. E o build consegue PROVAR que o
			// `-ldflags -X` funcionou: contra simbolo inexistente o linker
			// ignora em silencio e o binario sai sem chave, mas o valor
			// continua aparecendo no `strings` porque o Go grava os ldflags
			// no build info -- entao so perguntar ao binario em execucao
			// distingue os dois casos.
			//
			// Campo indisponivel e omitido: binario sem chave nao mostra o
			// campo, em vez de mostrar vazio.
			if update.ChavePublica != "" && update.ChavePublica != update.PlaceholderChavePublica {
				dados["update_public_key"] = update.ChavePublica
			}

			env.Data = dados
			return ctx.Renderer.Render(env)
		},
	}
}
