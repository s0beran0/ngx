// Package output define o envelope de saida do ngx, os diagnosticos e a
// traducao de erro para exit code. E a unica camada que serializa.
package output

// Version e a versao do ngx. Sobrescrita no build via -ldflags.
var Version = "0.1.0-dev"

// Severity classifica um diagnostico. Apenas SeverityError derruba o ok
// do envelope.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Diagnostic e um achado localizado. Os campos selector e id existem para
// que o agente aja sobre o achado sem reparsear a configuracao.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Column   int      `json:"column,omitempty"`
	Selector string   `json:"selector,omitempty"`
	ID       string   `json:"id,omitempty"`
	Docs     string   `json:"docs,omitempty"`
}

// Meta carrega dados sobre a execucao. ConfigHash ancora os IDs devolvidos
// nesta resposta: um ID so e valido contra o hash que veio junto com ele.
type Meta struct {
	DurationMS   int64  `json:"duration_ms"`
	NginxVersion string `json:"nginx_version,omitempty"`
	ConfigHash   string `json:"config_hash,omitempty"`
}

// Envelope e o formato unico de toda saida JSON do ngx.
type Envelope struct {
	OK         bool   `json:"ok"`
	Command    string `json:"command"`
	NgxVersion string `json:"ngx_version"`
	Data       any    `json:"data"`
	// Diagnostics nunca e nil: uma lista nula serializaria "diagnostics":null
	// e quebraria o `.diagnostics.length` de quem consome a saida. Construa
	// o envelope com New, que inicializa a lista vazia; nao monte um
	// Envelope{} literal sem preencher este campo.
	Diagnostics []Diagnostic `json:"diagnostics"`
	Meta        Meta         `json:"meta"`
}

// New cria um envelope de sucesso para o comando dado.
func New(command string) *Envelope {
	return &Envelope{
		OK:          true,
		Command:     command,
		NgxVersion:  Version,
		Diagnostics: []Diagnostic{},
	}
}

// AddDiagnostic anexa um diagnostico, derrubando ok se for um erro.
func (e *Envelope) AddDiagnostic(d Diagnostic) {
	e.Diagnostics = append(e.Diagnostics, d)
	if d.Severity == SeverityError {
		e.OK = false
	}
}
