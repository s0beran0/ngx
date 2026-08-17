// Package runtime opera o nginx do alvo: descobre qual binario existe e como
// foi compilado, roda `nginx -t` de forma estruturada e le a configuracao
// efetiva com `nginx -T`.
//
// Nada aqui executa nada por conta propria. Toda invocacao passa por um
// transport.Transport, entao o mesmo codigo — e o mesmo teste — vale para a
// maquina local e para um host remoto por SSH. Os parsers deste pacote nao
// sabem de onde os bytes vieram, e essa ignorancia e deliberada: e o que
// garante que a leitura remota nao seja um segundo caminho de codigo, com um
// segundo conjunto de defeitos.
//
// Duas invariantes atravessam o pacote:
//
//   - Codigo de saida diferente de zero e resultado, nao erro. Um `nginx -t`
//     que reprova a configuracao devolve um TestResult com OK false e err
//     nulo. Erro e o binario nao existir, a conexao cair ou o comando exigir
//     privilegio que nao foi concedido.
//
//   - Campo indisponivel e omitido, nunca estimado. Quem consome trata a
//     ausencia de um campo muito melhor que um numero errado.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"regexp"
	"strings"

	"github.com/s0beran0/ngx/internal/output"
	"github.com/s0beran0/ngx/internal/transport"
)

// Codigos de diagnostico do runtime. A faixa 0200-0299 e do transporte e do
// SSH; o runtime usa 0220 em diante, dentro dela, porque tudo que ele reporta
// nasce de um comando executado no alvo.
//
// A severidade nunca entra no codigo: o Diagnostic ja tem o campo severity, e
// repeti-la no prefixo criaria duas fontes de verdade.
const (
	// CodigoNginxAusente: nao ha binario nginx no alvo, ou ele nao e
	// executavel. Distinto de "o nginx rodou e reprovou".
	CodigoNginxAusente = "NGX-0220"

	// CodigoPrivilegioNecessario: o comando existe e rodou, mas o nginx
	// nao conseguiu ler o que precisava por falta de permissao. Sem --sudo
	// o ngx reporta e para — nao repete o comando com sudo (DR5).
	CodigoPrivilegioNecessario = "NGX-0221"

	// CodigoSudoIndisponivel: --sudo foi pedido, mas o sudo do alvo exige
	// senha, exige terminal ou nao existe. Como o ngx executa sem shell e
	// sem TTY, nao ha para onde mandar a senha.
	CodigoSudoIndisponivel = "NGX-0222"

	// CodigoSaidaNaoReconhecida: o comando rodou, mas a saida nao tem o
	// formato esperado. Inventar campos a partir de uma saida que nao se
	// entendeu e pior que admitir que nao se entendeu.
	CodigoSaidaNaoReconhecida = "NGX-0223"

	// CodigoTesteConfig: diagnostico traduzido de uma linha de `nginx -t`
	// ou `nginx -T`. Um unico codigo para todos os niveis: o nivel vira
	// severity, nao vira codigo.
	CodigoTesteConfig = "NGX-0224"

	// CodigoEstadoProcesso: algo sobre o estado do processo — a evidencia
	// de que ele nao esta rodando, ou a razao pela qual nao deu para saber.
	// Campo omitido sem este diagnostico junto seria degradar em silencio.
	CodigoEstadoProcesso = "NGX-0225"
)

// BinarioPadrao e o que o ngx executa quando ninguem diz outra coisa. Nome
// simples, resolvido pelo PATH do alvo: um caminho absoluto chutado aqui
// estaria errado na metade das distribuicoes.
const BinarioPadrao = "nginx"

// Runtime executa o nginx de um alvo atraves de um Transport.
type Runtime struct {
	tr      transport.Transport
	binario string
	sudo    bool
}

// Opcao configura um Runtime na construcao.
type Opcao func(*Runtime)

// ComBinario troca o binario invocado. Util quando o nginx nao esta no PATH
// do alvo ou quando ha mais de uma instalacao.
func ComBinario(caminho string) Opcao {
	return func(r *Runtime) {
		if caminho != "" {
			r.binario = caminho
		}
	}
}

// ComSudo liga a escalada explicita de privilegio (DR5). Sem ela, um comando
// que precise de privilegio e reportado, nunca repetido com sudo.
func ComSudo(ativo bool) Opcao {
	return func(r *Runtime) { r.sudo = ativo }
}

// New monta o runtime sobre um transporte.
func New(tr transport.Transport, opcoes ...Opcao) *Runtime {
	r := &Runtime{tr: tr, binario: BinarioPadrao}
	for _, o := range opcoes {
		o(r)
	}
	return r
}

// Alvo identifica contra o que este runtime opera, para o meta do envelope.
func (r *Runtime) Alvo() string { return r.tr.Describe() }

// argv monta a linha de comando. Sem shell, sem interpolacao: cada argumento
// e um elemento da lista.
//
// O sudo vai com -n (nao interativo) porque o ngx executa sem TTY. Um sudo
// que resolvesse pedir senha ficaria pendurado ate o timeout do contexto, e o
// operador veria uma lentidao inexplicavel em vez de uma recusa clara.
func (r *Runtime) argv(args ...string) []string {
	var out []string
	if r.sudo {
		out = append(out, "sudo", "-n")
	}
	out = append(out, r.binario)
	return append(out, args...)
}

// execucao e o resultado bruto de uma invocacao do nginx que chegou ao fim.
type execucao struct {
	argv   []string
	stdout string
	stderr string
	exit   int
}

// saida devolve stderr concatenado com stdout. O nginx escreve diagnostico em
// stderr, mas transportes que juntam os dois canais existem, e um parser que
// so olha um deles falharia silenciosamente nesses casos.
func (e *execucao) saida() string {
	if e.stderr == "" {
		return e.stdout
	}
	if e.stdout == "" {
		return e.stderr
	}
	return e.stderr + "\n" + e.stdout
}

// executar roda o nginx com os argumentos dados e classifica o que impede o
// resultado de existir: binario ausente, sudo indisponivel, privilegio
// faltando e falha de transporte. Codigo de saida diferente de zero por
// qualquer outra razao volta como execucao, com err nulo — e resultado.
func (r *Runtime) executar(ctx context.Context, args ...string) (*execucao, error) {
	argv := r.argv(args...)
	stdout, stderr, exit, err := r.tr.Run(ctx, argv)

	e := &execucao{
		argv:   argv,
		stdout: string(stdout),
		stderr: string(stderr),
		exit:   exit,
	}

	if err != nil {
		if binarioAusente(err) {
			return nil, erroNginxAusente(r, e, err)
		}
		return nil, output.Internal(err,
			"falha ao executar %s em %s", strings.Join(argv, " "), r.tr.Describe())
	}

	if exit == 0 {
		return e, nil
	}

	texto := e.saida()

	if r.sudo && sudoIndisponivel(texto) {
		return nil, erroSudoIndisponivel(r, e)
	}
	if naoEncontradoNaSaida(exit, texto) {
		return nil, erroNginxAusente(r, e, nil)
	}
	if exigePrivilegio(texto) {
		return nil, erroPrivilegio(r, e)
	}

	return e, nil
}

// binarioAusente reconhece a falha de transporte de "esse programa nao existe".
// O transporte local devolve exec.ErrNotFound para um nome do PATH e um
// fs.ErrNotExist para um caminho absoluto.
func binarioAusente(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

// naoEncontradoNaSaida reconhece o mesmo caso vindo de um shell remoto, onde
// o binario inexistente nao e erro de transporte: o ssh executa, o shell do
// alvo responde 127 e escreve a queixa em stderr.
func naoEncontradoNaSaida(exit int, texto string) bool {
	if exit != 127 {
		return false
	}
	t := strings.ToLower(texto)
	return strings.Contains(t, "command not found") ||
		strings.Contains(t, "no such file or directory") ||
		strings.Contains(t, "not found")
}

var padroesPrivilegio = []string{
	"permission denied",
	"operation not permitted",
	"must be run as root",
	"you must be root",
	"are you root",
}

// exigePrivilegio decide se a saida diz "faltou permissao". E deliberadamente
// conservador: reconhecer de menos faz o usuario ver a mensagem crua do nginx,
// que ainda e verdade; reconhecer demais transformaria um erro de sintaxe em
// pedido de privilegio, que e mentira.
func exigePrivilegio(texto string) bool {
	t := strings.ToLower(texto)
	for _, p := range padroesPrivilegio {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

var padroesSudo = []string{
	"sudo: a password is required",
	"a terminal is required",
	"no tty present",
	"is not in the sudoers file",
	"sudo: command not found",
	"sudo: not found",
}

func sudoIndisponivel(texto string) bool {
	t := strings.ToLower(texto)
	for _, p := range padroesSudo {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

// comandoPrivilegiado devolve a linha exata que o operador teria de autorizar.
// A DR5 exige dizer qual e o comando: "precisa de privilegio" sem o comando
// obriga quem le a adivinhar, e adivinhar em producao e como se escala
// privilegio por engano.
func comandoPrivilegiado(argv []string) string {
	if len(argv) > 0 && argv[0] == "sudo" {
		return strings.Join(argv, " ")
	}
	return "sudo -n " + strings.Join(argv, " ")
}

func erroPrivilegio(r *Runtime, e *execucao) error {
	var msg string
	if r.sudo {
		msg = fmt.Sprintf(
			"o comando `%s` foi executado com --sudo em %s e ainda assim nao teve "+
				"permissao para ler a configuracao. Saida do nginx: %s",
			strings.Join(e.argv, " "), r.tr.Describe(), resumo(e.saida()))
	} else {
		msg = fmt.Sprintf(
			"o comando `%s` exige privilegio em %s e o ngx nao escala sozinho: "+
				"repita com --sudo, que executa `%s`. Saida do nginx: %s",
			strings.Join(e.argv, " "), r.tr.Describe(),
			comandoPrivilegiado(e.argv), resumo(e.saida()))
	}
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoPrivilegioNecessario,
			Message:  msg,
		},
	}
}

func erroSudoIndisponivel(r *Runtime, e *execucao) error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoSudoIndisponivel,
			Message: fmt.Sprintf(
				"--sudo foi pedido, mas o sudo de %s nao pode ser usado sem interacao: %s. "+
					"O ngx executa sem shell e sem terminal, entao nao ha onde digitar senha; "+
					"libere o comando no sudoers ou rode o ngx como um usuario que ja tenha "+
					"acesso de leitura a configuracao",
				r.tr.Describe(), resumo(e.saida())),
		},
	}
}

func erroNginxAusente(r *Runtime, e *execucao, causa error) error {
	err := &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoNginxAusente,
			Message: fmt.Sprintf(
				"nao ha nginx executavel em %s: `%s` nao pode ser executado. "+
					"Se o binario existe com outro nome ou fora do PATH, informe o caminho",
				r.tr.Describe(), strings.Join(e.argv, " ")),
		},
		Err: causa,
	}
	return err
}

func erroSaidaNaoReconhecida(r *Runtime, e *execucao, oQue string) error {
	return &output.Error{
		Code: output.ExitInternal,
		Diag: output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoSaidaNaoReconhecida,
			Message: fmt.Sprintf(
				"a saida de `%s` em %s nao tem o formato esperado (%s): %s",
				strings.Join(e.argv, " "), r.tr.Describe(), oQue, resumo(e.saida())),
		},
	}
}

var espacos = regexp.MustCompile(`\s+`)

// resumo condensa uma saida multilinha numa linha curta, para caber numa
// mensagem de diagnostico sem destruir a legibilidade do JSON.
func resumo(texto string) string {
	t := strings.TrimSpace(espacos.ReplaceAllString(texto, " "))
	if t == "" {
		return "(sem saida)"
	}
	const limite = 300
	if len(t) > limite {
		return t[:limite] + "..."
	}
	return t
}
