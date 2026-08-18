// Package output defines the ngx output envelope, the diagnostics and the
// translation from error to exit code. It is the only layer that serializes.
package output

// Version is the ngx version. Overridden at build time via -ldflags.
var Version = "0.1.0-dev"

// Severity classifies a diagnostic. Only SeverityError brings down the
// envelope's ok.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Diagnostic is a located finding. The selector and id fields exist so that
// the agent can act on the finding without reparsing the configuration.
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

// Meta carries data about the execution. ConfigHash anchors the IDs returned
// in this response: an ID is only valid against the hash that came with it.
//
// Target is the transport's Describe() -- "local" or "ssh://user@host:port".
// Whoever consumes the output needs to know what the tool operated against:
// the same response, coming from another machine, is a different fact. It is
// omitted, never estimated, when the transport never even came to exist
// (failure before connecting): absence is information, the wrong target is a
// lie.
type Meta struct {
	DurationMS   int64  `json:"duration_ms"`
	NginxVersion string `json:"nginx_version,omitempty"`
	ConfigHash   string `json:"config_hash,omitempty"`
	Target       string `json:"target,omitempty"`
}

// Envelope is the single format of every JSON output from ngx.
type Envelope struct {
	OK         bool   `json:"ok"`
	Command    string `json:"command"`
	NgxVersion string `json:"ngx_version"`
	Data       any    `json:"data"`
	// Diagnostics is never nil: a null list would serialize as
	// "diagnostics":null and would break the `.diagnostics.length` of
	// whoever consumes the output. Build the envelope with New, which
	// initializes the empty list; do not assemble a literal Envelope{}
	// without filling this field.
	Diagnostics []Diagnostic `json:"diagnostics"`
	Meta        Meta         `json:"meta"`
}

// New creates a success envelope for the given command.
func New(command string) *Envelope {
	return &Envelope{
		OK:          true,
		Command:     command,
		NgxVersion:  Version,
		Diagnostics: []Diagnostic{},
	}
}

// AddDiagnostic appends a diagnostic, bringing ok down if it is an error.
func (e *Envelope) AddDiagnostic(d Diagnostic) {
	e.Diagnostics = append(e.Diagnostics, d)
	if d.Severity == SeverityError {
		e.OK = false
	}
}
