package output

import (
	"fmt"
	"strings"
)

// RedactedValue substitui o valor de uma diretiva sensivel. A diretiva, o id
// e a linha permanecem visiveis: sumir com o no inteiro faria o agente
// concluir que a diretiva nao existe, o que e pior que esconder o valor.
const RedactedValue = "***"

// Redactable e implementado por qualquer dado que saiba produzir uma copia
// redigida de si mesmo. A redacao acontece na serializacao, nunca na arvore
// em memoria: se a arvore fosse redigida no parse, fmt gravaria *** dentro
// do .conf do usuario.
type Redactable interface {
	Redacted(rs RedactSet) any
}

// RedactRule casa uma diretiva pelo nome, opcionalmente exigindo um prefixo
// de argumentos.
type RedactRule struct {
	Directive string
	ArgPrefix []string
}

// ParseRedactRule le uma entrada de output.redact. Aceita os tres formatos
// que a spec usa: nome de diretiva, nome com prefixo de argumentos, e o
// prefixo de contexto "**." — que e redundante, porque regras ja valem em
// qualquer contexto, mas e aceito para nao quebrar configuracoes escritas a
// partir da spec.
func ParseRedactRule(s string) (RedactRule, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "**.")

	campos := strings.Fields(s)
	if len(campos) == 0 {
		return RedactRule{}, fmt.Errorf("regra de redacao vazia")
	}

	r := RedactRule{Directive: campos[0]}
	if len(campos) > 1 {
		r.ArgPrefix = campos[1:]
	}
	return r, nil
}

// Matches informa se a diretiva dada deve ter seu valor redigido.
func (r RedactRule) Matches(directive string, args []string) bool {
	if r.Directive != directive {
		return false
	}
	if len(args) < len(r.ArgPrefix) {
		return false
	}
	for i, p := range r.ArgPrefix {
		if args[i] != p {
			return false
		}
	}
	return true
}

// RedactSet e o conjunto de regras ativas.
type RedactSet struct {
	rules []RedactRule
}

// NewRedactSet compila as entradas de output.redact.
func NewRedactSet(entradas []string) (RedactSet, error) {
	var set RedactSet
	for _, e := range entradas {
		r, err := ParseRedactRule(e)
		if err != nil {
			return RedactSet{}, fmt.Errorf("regra de redacao %q: %w", e, err)
		}
		set.rules = append(set.rules, r)
	}
	return set, nil
}

// Empty informa se nenhuma regra esta ativa.
func (s RedactSet) Empty() bool { return len(s.rules) == 0 }

// Matches informa se alguma regra casa a diretiva dada.
func (s RedactSet) Matches(directive string, args []string) bool {
	for _, r := range s.rules {
		if r.Matches(directive, args) {
			return true
		}
	}
	return false
}
