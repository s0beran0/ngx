// Package settings loads the configuration file of ngx itself.
// v0.1 reads only the subset its commands use; keys from future versions are
// ignored without error, so that a file written from the complete spec works
// today.
package settings

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Nginx points to the binary and the main configuration.
type Nginx struct {
	Binary string `koanf:"binary"`
	Config string `koanf:"config"`
}

// Output controls format and redaction.
type Output struct {
	Format string   `koanf:"format"`
	Redact []string `koanf:"redact"`
}

// chaveRedact is the koanf path of Output.Redact, used in Load() to decide
// when the declared list replaces the default one. It is extracted as a
// constant because renaming the `koanf:"redact"` or `koanf:"output"` tag
// without updating this value would break the replacement silently, with no
// compile error.
const chaveRedact = "output.redact"

// Settings is the effective ngx configuration.
type Settings struct {
	Nginx  Nginx  `koanf:"nginx"`
	Output Output `koanf:"output"`
}

// Defaults returns the configuration used when no file exists. Redaction
// comes turned on: without it, a get may leak the path of a private key into
// the context of an LLM running on a third-party API.
func Defaults() *Settings {
	return &Settings{
		Output: Output{
			Format: "auto",
			Redact: []string{
				"ssl_certificate_key",
				"proxy_set_header Authorization",
				"auth_basic_user_file",
			},
		},
	}
}

// Load merges the global file with the local one, with the local one winning
// key by key. A missing file is not an error.
func Load(globalPath, localPath string) (*Settings, error) {
	k := koanf.New(".")

	for _, p := range []string{globalPath, localPath} {
		if p == "" {
			continue
		}
		if err := k.Load(file.Provider(p), yaml.Parser()); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("while loading %s: %w", p, err)
		}
	}

	s := Defaults()

	// mapstructure (used by koanf in Unmarshal) reuses the non-nil slice
	// already present in the destination struct and fills it by index,
	// instead of allocating it from scratch. That would leave leftover
	// defaults in the tail whenever the user's list is shorter than the
	// default one -- for example, a 1-item list on top of the 3 defaults
	// would leave the last 2 defaults untouched. We zero it here to force a
	// total replacement, regardless of how the pinned version of
	// mapstructure behaves.
	//
	// The zeroing must only happen when the user actually declared a list
	// (even an empty one). If the key is absent, or present but null -- the
	// typical case of a file where the person commented out every item of
	// the list -- k.Get returns nil and the defaults must survive;
	// otherwise redaction would silently turn off, failing open on a
	// security feature.
	if v := k.Get(chaveRedact); v != nil {
		s.Output.Redact = nil
	}

	if err := k.Unmarshal("", s); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	return s, nil
}
