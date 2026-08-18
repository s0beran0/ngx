package output

import (
	"encoding/json"
	"fmt"
	"io"
)

// Format seleciona o renderizador. FormatAuto decide por TTY.
type Format string

const (
	FormatAuto  Format = "auto"
	FormatJSON  Format = "json"
	FormatHuman Format = "human"
)

// HumanRenderable e implementado por dados que sabem se apresentar a um
// humano. Dados que nao implementam caem para JSON indentado, que e mais
// util que imprimir a struct crua do Go.
type HumanRenderable interface {
	RenderHuman(w io.Writer) error
}

// Renderer serializa o envelope. E a unica camada que escreve saida.
//
// A redacao cobre apenas o campo Data do envelope. Diagnostics e Meta nunca
// passam pela redacao: Diagnostic.Message e texto livre e nao ha como saber
// qual pedaco e sensivel, entao a mitigacao e de politica, nao de codigo — e
// responsabilidade de quem produz um Diagnostic nunca embutir valor de
// diretiva na mensagem.
type Renderer struct {
	Out    io.Writer
	Format Format
	IsTTY  bool
	// Redact e o conjunto de regras ativas. A redacao alcanca somente o
	// Redactable de Data no nivel mais alto: se Data implementa Redactable,
	// Render troca Data pelo resultado de Redacted antes de serializar. A
	// redacao NAO e recursiva — nao desce em campos, slices ou mapas dentro
	// de Data por conta propria. Qualquer tipo usado em Data que possa
	// carregar um valor sensivel (por exemplo, uma arvore de configuracao
	// inteira) precisa implementar Redactable e, dentro do proprio metodo,
	// propagar a redacao para os seus filhos. Um Data que nao implemente
	// Redactable sai integro, sem erro e sem aviso, mesmo com Redact
	// preenchido.
	Redact   RedactSet
	NoRedact bool
	Quiet    bool
}

// Render escreve o envelope no formato resolvido. Nao muta o Data do
// envelope recebido: a versao redigida (quando houver) e usada apenas para
// a escrita, e o chamador continua vendo o dado original depois da chamada.
func (r *Renderer) Render(env *Envelope) error {
	if r.NoRedact && !r.IsTTY {
		return Usage("--no-redact so e aceito quando a saida e um terminal")
	}

	// --quiet suprime o sucesso, nao o aviso. Um envelope ok=true pode
	// carregar diagnostico de seguranca -- host key aceita sem verificacao,
	// chave recusada com queda calada para senha -- e engoli-lo faria o
	// escape virar silencioso, que e exatamente o que a DR1 proibe. Quem
	// pede --quiet quer menos ruido, nao menos alerta.
	if r.Quiet && env.OK && !temAvisoOuPior(env.Diagnostics) {
		return nil
	}

	format, err := r.resolveFormat()
	if err != nil {
		return err
	}

	data := env.Data
	if !r.NoRedact && !r.Redact.Empty() {
		if red, ok := data.(Redactable); ok {
			data = red.Redacted(r.Redact)
		}
	}

	// out e uma copia local do envelope com o Data (possivelmente redigido)
	// substituido; env.Data do chamador permanece intocado.
	out := *env
	out.Data = data

	switch format {
	case FormatHuman:
		return r.renderHuman(&out)
	default:
		return r.renderJSON(&out)
	}
}

// resolveFormat decide o formato efetivo. FormatAuto (ou o zero value) decide
// por IsTTY. Qualquer valor fora de auto/json/human e erro de uso: Format
// costuma vir de output.format no arquivo de configuracao, que e string
// livre, e um valor invalido nao pode virar JSON em silencio.
func (r *Renderer) resolveFormat() (Format, error) {
	switch r.Format {
	case FormatAuto, "":
		if r.IsTTY {
			return FormatHuman, nil
		}
		return FormatJSON, nil
	case FormatJSON, FormatHuman:
		return r.Format, nil
	default:
		return "", Usage("formato de saida invalido: %q", string(r.Format))
	}
}

func (r *Renderer) renderJSON(env *Envelope) error {
	enc := json.NewEncoder(r.Out)
	if r.IsTTY {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(env); err != nil {
		return Internal(err, "falha ao serializar a saida")
	}
	return nil
}

func (r *Renderer) renderHuman(env *Envelope) error {
	for _, d := range env.Diagnostics {
		loc := ""
		if d.File != "" {
			// Line e Column sao opcionais por design (omitempty): sem uma
			// linha valida, anexar ":0:0" seria uma coordenada falsa.
			if d.Line > 0 {
				switch {
				case d.Line > 0 && d.Column > 0:
					loc = fmt.Sprintf(" %s:%d:%d", d.File, d.Line, d.Column)
				case d.Line > 0:
					loc = fmt.Sprintf(" %s:%d", d.File, d.Line)
				default:
					// Sem posicao conhecida: so o arquivo. `arquivo:0:0`
					// aparenta defeito e nao aponta lugar nenhum.
					loc = fmt.Sprintf(" %s", d.File)
				}
			} else {
				loc = fmt.Sprintf(" %s", d.File)
			}
		}
		if _, err := fmt.Fprintf(r.Out, "%s: %s%s\n", d.Severity, d.Message, loc); err != nil {
			return Internal(err, "falha ao escrever diagnostico")
		}
	}

	if hr, ok := env.Data.(HumanRenderable); ok {
		if err := hr.RenderHuman(r.Out); err != nil {
			return Internal(err, "falha ao renderizar saida humana")
		}
		return nil
	}

	if env.Data == nil {
		return nil
	}
	b, err := json.MarshalIndent(env.Data, "", "  ")
	if err != nil {
		return Internal(err, "falha ao serializar a saida")
	}
	if _, err := fmt.Fprintln(r.Out, string(b)); err != nil {
		return Internal(err, "falha ao escrever saida")
	}
	return nil
}

// temAvisoOuPior diz se ha diagnostico que nao pode ser suprimido por
// --quiet. Severidade info e informativa e cai no silencio; warning e error
// sao sinal, e sinal suprimido e sinal inexistente.
func temAvisoOuPior(diags []Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == SeverityWarning || d.Severity == SeverityError {
			return true
		}
	}
	return false
}
