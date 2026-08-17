package output

import (
	"fmt"
	"regexp"
	"strings"
)

// nomeDeDiretivaValido casa o alfabeto real de nomes de diretiva do nginx:
// letras, digitos e underscore. Qualquer coisa fora disso (ponto-e-virgula
// copiado do .conf, "*", ".", "/") indica uma regra digitada errado, e e
// melhor falhar alto no parse do que deixar uma regra morta que nunca casa
// nada silenciosamente.
var nomeDeDiretivaValido = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

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
//
// Cada campo pode vir envolto num unico par de aspas simples ou duplas (ex.:
// proxy_set_header "X-Api-Key"); esse par e removido antes de validar. O
// nome da diretiva (primeiro campo, ja sem aspas e sem o prefixo "**.") e
// validado contra o alfabeto real de nomes de diretiva do nginx — qualquer
// coisa fora de letras/digitos/underscore e erro, nao regra morta.
func ParseRedactRule(s string) (RedactRule, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "**.")

	brutos := strings.Fields(s)
	if len(brutos) == 0 {
		return RedactRule{}, fmt.Errorf("regra de redacao vazia")
	}

	campos := make([]string, len(brutos))
	for i, c := range brutos {
		campos[i] = semAspasCircundantes(c)
	}

	if !nomeDeDiretivaValido.MatchString(campos[0]) {
		return RedactRule{}, fmt.Errorf("nome de diretiva invalido: %q", campos[0])
	}

	r := RedactRule{Directive: campos[0]}
	if len(campos) > 1 {
		r.ArgPrefix = campos[1:]
	}
	return r, nil
}

// semAspasCircundantes remove um unico par de aspas simples ou duplas que
// envolva o campo inteiro. strings.Fields nao entende aspas, entao um campo
// como "X-Api-Key" chegaria aqui com as aspas literais e nunca casaria um
// argumento real.
func semAspasCircundantes(campo string) string {
	if len(campo) < 2 {
		return campo
	}
	primeiro, ultimo := campo[0], campo[len(campo)-1]
	if (primeiro == '"' && ultimo == '"') || (primeiro == '\'' && ultimo == '\'') {
		return campo[1 : len(campo)-1]
	}
	return campo
}

// Matches informa se a diretiva dada deve ter seu valor redigido. O nome da
// diretiva e comparado exatamente, porque diretivas nginx sao sempre
// minusculas. Os argumentos sao comparados sem diferenciar caixa, porque
// nomes de header HTTP sao case-insensitive e o nginx propaga a caixa como
// foi escrita no .conf: uma regra "proxy_set_header Authorization" precisa
// casar tambem "authorization" ou "AUTHORIZATION", senao o token vaza
// inteiro. O custo e redigir demais se um prefixo de argumento for, por
// acaso, um caminho de arquivo que difere so na caixa — lado seguro do
// trade-off.
func (r RedactRule) Matches(directive string, args []string) bool {
	if r.Directive != directive {
		return false
	}
	if len(args) < len(r.ArgPrefix) {
		return false
	}
	for i, p := range r.ArgPrefix {
		if !strings.EqualFold(args[i], p) {
			return false
		}
	}
	return true
}

// RedactSet e o conjunto de regras ativas.
type RedactSet struct {
	rules []RedactRule
}

// NewRedactSet compila as entradas de output.redact. O erro deve ser tratado
// como fatal, nao como aviso: no caminho de erro a funcao devolve um
// RedactSet{} zero-value cujo Matches e sempre false, ou seja, ZERO
// redacao — nao redacao parcial das regras validas ate ali. Um consumidor
// que apenas logue o erro e siga adiante fica rodando sem nenhuma protecao
// contra vazamento de segredo.
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
