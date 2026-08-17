package config_test

import (
	"testing"

	"github.com/eduardoborges/ngx/internal/config"
)

// O fuzz garante que, para qualquer entrada que o tokenizador aceite, os
// spans continuam apontando para o texto real e em ordem crescente. E a rede
// que sustenta a edicao cirurgica da v0.2.
func FuzzTokenizeSpans(f *testing.F) {
	f.Add("server { listen 80; }")
	f.Add(`add_header X "a; b";`)
	f.Add("# comentario\nhttp { }")
	f.Add(`location ~ \.php$ { }`)
	f.Add("map $a $b {\n default 0;\n}")

	f.Fuzz(func(t *testing.T, s string) {
		toks, err := config.Tokenize([]byte(s))
		if err != nil {
			return
		}

		prev := 0
		for _, tok := range toks {
			if tok.Start < prev {
				t.Fatalf("token comeca em %d, antes do fim anterior %d", tok.Start, prev)
			}
			if tok.End > len(s) || tok.Start > tok.End {
				t.Fatalf("span invalido [%d,%d) para fonte de %d bytes", tok.Start, tok.End, len(s))
			}
			if got := s[tok.Start:tok.End]; got != tok.Raw {
				t.Fatalf("raw %q difere da fonte %q em [%d,%d)", tok.Raw, got, tok.Start, tok.End)
			}
			if tok.Line < 1 || tok.Column < 1 {
				t.Fatalf("linha/coluna base zero: %d:%d", tok.Line, tok.Column)
			}
			prev = tok.End
		}
	})
}
