package cli

import (
	"fmt"
	"io"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/runtime"
	"github.com/s0beran0/ngx/internal/transport"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// ConectarSSH abre um transporte remoto e devolve, junto, o que a montagem
// observou pelo caminho — host key aceita sem verificacao, ssh-agent
// indisponivel, chave ilegivel.
//
// E um campo do Context, e nao uma chamada direta a transport, pelo mesmo
// motivo que GlobalSettingsPath e campo: um teste do CLI precisa exercitar a
// fiacao das flags sem abrir um socket. Em producao o valor e sempre
// transport.SSHComDiagnosticos.
type ConectarSSH func(transport.SSHOptions) (transport.Transport, []output.Diagnostic, error)

// flagsDeConexao sao as flags que so fazem sentido com --host. Passar
// qualquer uma delas sem destino e erro de uso, nao um valor ignorado em
// silencio: quem digitou --user deploy sem --host acredita que a conexao vai
// usar aquele usuario.
//
// --sudo fica de fora de proposito: privilegio explicito (DR5) vale tambem
// para o alvo local.
var flagsDeConexao = []string{"host", "user", "port", "key", "known-hosts", "insecure-host-key"}

// registrarFlagsDeConexao adiciona as flags globais de acesso remoto.
//
// Nao existe flag de senha, e isso e decisao de seguranca, nao esquecimento:
// o valor de uma flag aparece em `ps`, no historico do shell e no log de
// qualquer CI. O segredo vem de NGX_SSH_PASSWORD ou de um prompt sem eco,
// ambos resolvidos dentro de transport.MontarAutenticacao.
//
// --port nasce em 0, e nao em 22, porque zero e o que distingue "nao
// informado" de "informado como 22". A precedencia da DR2 depende dessa
// distincao: flag explicita vence o ~/.ssh/config, que vence o default.
func registrarFlagsDeConexao(p *pflag.FlagSet, f *GlobalFlags) {
	p.StringVar(&f.Host, "host", "", "opera num host remoto por SSH (alias do ~/.ssh/config ou endereco)")
	p.StringVar(&f.User, "user", "", "usuario SSH")
	p.IntVar(&f.Port, "port", 0, "porta SSH")
	p.StringVar(&f.Key, "key", "", "caminho da chave privada")
	p.StringVar(&f.KnownHosts, "known-hosts", "", "caminho do known_hosts")
	p.BoolVar(&f.InsecureHostKey, "insecure-host-key", false,
		"aceita a host key sem verificar (inseguro; produz aviso na saida)")
	p.BoolVar(&f.Sudo, "sudo", false, "escala privilegio nos comandos que exigirem")
}

// abrirTransporte decide o alvo da execucao e o guarda no Context.
//
// Sem --host o caminho e o de sempre: transporte local, nenhuma resolucao de
// ~/.ssh/config, nenhum socket. Toda a v0.1 e uso local — uma regressao aqui
// quebraria o que ja funciona para servir o que ainda ninguem usa.
//
// Os diagnosticos ficam no Context, e nao sao devolvidos apenas para quem
// chamou, porque eles precisam alcancar o envelope tanto no caminho de
// sucesso quanto no de erro. Um aviso de --insecure-host-key que suma da
// saida faz o escape da DR1 virar silencioso, que e exatamente o que a
// decisao existe para impedir.
func abrirTransporte(ctx *Context, cmd *cobra.Command) error {
	f := ctx.Flags

	if f.Host == "" {
		if err := recusarFlagsDeConexaoSemHost(cmd); err != nil {
			return err
		}
		ctx.Transport = transport.Local()
		return nil
	}

	opts := transport.SSHOptions{
		Host:            f.Host,
		Port:            f.Port,
		User:            f.User,
		KeyPath:         f.Key,
		KnownHostsPath:  f.KnownHosts,
		InsecureHostKey: f.InsecureHostKey,
		Timeout:         f.Timeout,
		// Password fica vazio de proposito: transport.MontarAutenticacao le
		// NGX_SSH_PASSWORD ou pergunta no terminal. Nenhum segredo atravessa
		// a linha de comando.
	}

	caminhoConfig, diagCaminho := caminhoSSHConfig(ctx)
	if diagCaminho != nil {
		ctx.TransportDiags = append(ctx.TransportDiags, *diagCaminho)
	}

	// A precedencia da DR2 e inteira do transport: flag explicita vence o
	// ~/.ssh/config, que vence o default. Reimplementar isso aqui criaria
	// uma segunda fonte de verdade que pode discordar da primeira.
	resolvido, diags, err := transport.ResolverSSHConfig(opts, caminhoConfig)
	ctx.TransportDiags = append(ctx.TransportDiags, diags...)
	if err != nil {
		return err
	}

	tr, diagsConexao, err := ctx.conectar()(resolvido)
	ctx.TransportDiags = append(ctx.TransportDiags, diagsConexao...)
	if err != nil {
		return err
	}

	ctx.Transport = tr
	return nil
}

// recusarFlagsDeConexaoSemHost transforma em erro de uso o que seria uma
// surpresa silenciosa. Usa Changed, e nao o valor, para pegar tambem
// --user "" e --port 0 digitados explicitamente.
func recusarFlagsDeConexaoSemHost(cmd *cobra.Command) error {
	if cmd == nil {
		return nil
	}
	for _, nome := range flagsDeConexao {
		if nome == "host" {
			continue
		}
		if flag := cmd.Flags().Lookup(nome); flag != nil && flag.Changed {
			return output.Usage("--%s so faz sentido junto de --host", nome)
		}
	}
	return nil
}

// caminhoSSHConfig devolve o arquivo a consultar. Um Context com o campo
// preenchido manda — e o que permite testar a resolucao sem depender do HOME
// de quem roda os testes.
//
// Nao conseguir localizar o diretorio do usuario nao aborta a conexao: a
// resolucao segue com flags e defaults, e o aviso (DR7) diz por que o
// ~/.ssh/config nao foi consultado. Abortar quebraria quem passou --host,
// --user e --port explicitamente e nao precisa do arquivo para nada.
func caminhoSSHConfig(ctx *Context) (string, *output.Diagnostic) {
	if ctx.SSHConfigPath != "" {
		return ctx.SSHConfigPath, nil
	}

	caminho, err := transport.CaminhoSSHConfigPadrao()
	if err != nil {
		return "", &output.Diagnostic{
			Severity: output.SeverityWarning,
			Code:     transport.CodigoAvisoSSHConfig,
			Message: fmt.Sprintf(
				"o ~/.ssh/config nao foi consultado (%v); valendo apenas as flags e os defaults",
				err,
			),
		}
	}
	return caminho, nil
}

// conectar devolve o conector a usar. O default de producao mora aqui, e nao
// em Execute, para que um Context montado a mao por um teste de outro assunto
// continue funcionando.
//
// E SSHComDiagnosticos, nunca SSH: a segunda descarta os diagnosticos de host
// key e de ssh-agent, e um aviso perdido e um aviso que nao existe.
func (c *Context) conectar() ConectarSSH {
	if c.ConectarSSH != nil {
		return c.ConectarSSH
	}
	return transport.SSHComDiagnosticos
}

// transporte devolve o alvo das operacoes, caindo no local quando o Context
// foi montado sem passar por preparar (testes de outros assuntos).
func (c *Context) transporte() transport.Transport {
	if c.Transport == nil {
		return transport.Local()
	}
	return c.Transport
}

// NovoEnvelope cria o envelope do comando ja com o alvo no meta e com os
// diagnosticos da conexao dentro.
//
// Todo comando monta a saida por aqui em vez de chamar output.New direto:
// alvo e avisos de conexao valem para qualquer comando, e o que cada comando
// tem que lembrar de fazer, algum comando esquece.
func (c *Context) NovoEnvelope(comando string) *output.Envelope {
	env := output.New(comando)
	if c.Transport != nil {
		env.Meta.Target = c.Transport.Describe()
	}
	for _, d := range c.TransportDiags {
		env.AddDiagnostic(d)
	}
	return env
}

// NovoRuntime monta o runtime sobre o transporte do contexto.
//
// ComSudo carrega a flag --sudo diretamente: sem ela, um comando que precise
// de privilegio e reportado, nunca repetido com sudo (DR5).
func (c *Context) NovoRuntime() *runtime.Runtime {
	if c.Flags == nil {
		return runtime.New(c.transporte())
	}
	return runtime.New(c.transporte(),
		runtime.ComBinario(c.Flags.NginxBin),
		runtime.ComSudo(c.Flags.Sudo),
	)
}

// fecharTransporte libera a conexao. Chamar duas vezes e seguro pelo contrato
// do Transport, e o campo e zerado para que um Context reaproveitado nao
// aponte para um transporte morto.
func (c *Context) fecharTransporte() error {
	if c.Transport == nil {
		return nil
	}
	tr := c.Transport
	c.Transport = nil
	return tr.Close()
}

// avisarFalhaAoFechar e o ultimo recurso para um Close que falhou depois de o
// envelope ja ter sido escrito. O envelope e imutavel a essa altura, e uma
// conexao que nao fechou direito nao muda o resultado do comando — mas
// tambem nao pode desaparecer.
func avisarFalhaAoFechar(stderr io.Writer, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(stderr, "ngx: falha ao encerrar a conexao: %v\n", err)
}
