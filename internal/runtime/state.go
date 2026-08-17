package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"

	"github.com/s0beran0/ngx/internal/output"
)

// State e o que se consegue afirmar sobre o processo do nginx no alvo.
//
// O que nao esta aqui e tao deliberado quanto o que esta. Nao ha campo de
// numero de workers nem de horario em que o master carregou a configuracao:
// os dois dependem de inspecao de processo, que diverge entre Linux e darwin
// e e fragil por SSH. Um campo que so as vezes existe e pior que campo
// nenhum, e um numero estimado e pior que os dois — um agente confia no que
// le. Quando esses dados tiverem uma fonte confiavel, viram campos; ate la,
// nao existem.
type State struct {
	// Running e ponteiro porque tem tres estados, nao dois: rodando, nao
	// rodando, e "nao deu para saber" — que sai como campo ausente, nunca
	// como false. Reportar false sem evidencia diria que o nginx caiu.
	Running *bool `json:"running,omitempty"`

	// MasterPID e omitido quando o pidfile nao existe ou nao pode ser lido.
	MasterPID int `json:"master_pid,omitempty"`

	// PIDFile e o caminho consultado.
	PIDFile string `json:"pid_file,omitempty"`

	// Diagnostics explica toda indisponibilidade. A DR5 exige distinguir
	// "nao consegui ler" de "nao existe", e nunca degradar em silencio:
	// campo ausente sem diagnostico seria exatamente o silencio proibido.
	// Nunca e nil.
	Diagnostics []output.Diagnostic `json:"diagnostics"`
}

// State le o pidfile pelo transporte e confere se o processo existe.
//
// A leitura do pidfile passa pelo Open do transporte (SFTP no caso remoto), e
// a verificacao de existencia usa `kill -0`, que nao envia sinal nenhum:
// pergunta ao sistema se o processo esta la. E o unico jeito portavel de
// separar um pidfile obsoleto de um master vivo sem depender de /proc.
func (r *Runtime) State(ctx context.Context, pidPath string) (*State, error) {
	s := &State{PIDFile: pidPath, Diagnostics: []output.Diagnostic{}}

	if pidPath == "" {
		s.diag(output.SeverityWarning, CodigoEstadoProcesso,
			"o caminho do pidfile nao e conhecido, entao o estado do processo nao pode ser apurado")
		return s, nil
	}

	f, err := r.tr.Open(pidPath)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// Ausencia do pidfile e evidencia, nao suposicao: o nginx
			// remove o arquivo ao parar. Por isso aqui Running vira
			// false, e nos demais casos fica ausente.
			naoRoda := false
			s.Running = &naoRoda
			s.diag(output.SeverityInfo, CodigoEstadoProcesso,
				fmt.Sprintf("o pidfile %s nao existe, entao o master nao esta rodando", pidPath))
		case errors.Is(err, fs.ErrPermission):
			s.diag(output.SeverityWarning, CodigoPrivilegioNecessario,
				fmt.Sprintf("o pidfile %s existe mas nao pode ser lido por falta de permissao em %s; "+
					"o estado do processo fica indisponivel ate o ngx rodar com acesso de leitura a esse arquivo",
					pidPath, r.tr.Describe()))
		default:
			s.diag(output.SeverityWarning, CodigoEstadoProcesso,
				fmt.Sprintf("o pidfile %s nao pode ser lido em %s: %v", pidPath, r.tr.Describe(), err))
		}
		return s, nil
	}
	defer f.Close()

	conteudo, err := io.ReadAll(f)
	if err != nil {
		s.diag(output.SeverityWarning, CodigoEstadoProcesso,
			fmt.Sprintf("falha ao ler o pidfile %s em %s: %v", pidPath, r.tr.Describe(), err))
		return s, nil
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(conteudo)))
	if err != nil || pid <= 0 {
		s.diag(output.SeverityWarning, CodigoEstadoProcesso,
			fmt.Sprintf("o pidfile %s nao contem um pid: %q", pidPath, resumo(string(conteudo))))
		return s, nil
	}
	s.MasterPID = pid

	// kill -0 nao sinaliza o processo; so consulta. Nao leva sudo: perguntar
	// se um pid existe nao exige privilegio, e escalar aqui contrariaria a
	// DR5 sem ganho nenhum.
	_, stderr, exit, err := r.tr.Run(ctx, []string{"kill", "-0", strconv.Itoa(pid)})
	if err != nil {
		s.diag(output.SeverityWarning, CodigoEstadoProcesso,
			fmt.Sprintf("nao foi possivel conferir se o pid %d esta vivo em %s: %v",
				pid, r.tr.Describe(), err))
		return s, nil
	}

	texto := string(stderr)
	switch {
	case exit == 0:
		roda := true
		s.Running = &roda
	case exigePrivilegio(texto):
		// Processo existe mas pertence a outro usuario. Afirmar que nao
		// roda seria falso; afirmar que roda seria adivinhar a partir da
		// mensagem. Fica indisponivel, com o motivo dito.
		s.diag(output.SeverityWarning, CodigoPrivilegioNecessario,
			fmt.Sprintf("o pid %d existe mas pertence a outro usuario, e conferir seu estado "+
				"em %s exige privilegio", pid, r.tr.Describe()))
	default:
		naoRoda := false
		s.Running = &naoRoda
		s.diag(output.SeverityWarning, CodigoEstadoProcesso,
			fmt.Sprintf("o pidfile %s aponta para o pid %d, que nao existe: pidfile obsoleto",
				pidPath, pid))
	}

	return s, nil
}

func (s *State) diag(sev output.Severity, codigo, mensagem string) {
	s.Diagnostics = append(s.Diagnostics, output.Diagnostic{
		Severity: sev,
		Code:     codigo,
		Message:  mensagem,
		File:     s.PIDFile,
	})
}
