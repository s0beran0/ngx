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
type Renderer struct {
	Out      io.Writer
	Format   Format
	IsTTY    bool
	Redact   RedactSet
	NoRedact bool
	Quiet    bool
}

// Render escreve o envelope no formato resolvido.
func (r *Renderer) Render(env *Envelope) error {
	if r.NoRedact && !r.IsTTY {
		return Usage("--no-redact so e aceito quando a saida e um terminal")
	}

	if r.Quiet && env.OK {
		return nil
	}

	if !r.NoRedact && !r.Redact.Empty() {
		if red, ok := env.Data.(Redactable); ok {
			env.Data = red.Redacted(r.Redact)
		}
	}

	switch r.resolveFormat() {
	case FormatHuman:
		return r.renderHuman(env)
	default:
		return r.renderJSON(env)
	}
}

func (r *Renderer) resolveFormat() Format {
	if r.Format == FormatAuto || r.Format == "" {
		if r.IsTTY {
			return FormatHuman
		}
		return FormatJSON
	}
	return r.Format
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
			loc = fmt.Sprintf(" %s:%d:%d", d.File, d.Line, d.Column)
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
