package config

import "fmt"

// TokenKind classifica um token.
type TokenKind int

const (
	// TokenWord e um nome de diretiva ou um argumento.
	TokenWord TokenKind = iota
	TokenSemicolon
	TokenBlockStart
	TokenBlockEnd
	TokenComment
)

// Token e um lexema com sua posicao exata em bytes. Value e o conteudo
// semantico (sem aspas, sem o # do comentario); Raw e o texto original.
type Token struct {
	Kind   TokenKind
	Value  string
	Raw    string
	Start  int
	End    int
	Line   int
	Column int
	Quoted bool
}

type tokenizer struct {
	src    []byte
	pos    int
	line   int
	col    int
	tokens []Token
}

// Tokenize quebra a fonte em tokens com offsets de byte. Nao interpreta
// diretiva nenhuma: so precisa saber onde cada lexema comeca e termina,
// respeitando aspas, escapes e comentarios.
func Tokenize(src []byte) ([]Token, error) {
	t := &tokenizer{src: src, line: 1, col: 1}
	for {
		t.pularEspacos()
		if t.pos >= len(t.src) {
			return t.tokens, nil
		}
		if err := t.proximo(); err != nil {
			return nil, err
		}
	}
}

func (t *tokenizer) pularEspacos() {
	for t.pos < len(t.src) && ehEspaco(t.src[t.pos]) {
		t.avancar()
	}
}

func (t *tokenizer) avancar() {
	if t.src[t.pos] == '\n' {
		t.line++
		t.col = 1
	} else {
		t.col++
	}
	t.pos++
}

func (t *tokenizer) proximo() error {
	start, line, col := t.pos, t.line, t.col

	switch c := t.src[t.pos]; {
	case c == ';':
		t.avancar()
		t.emitir(TokenSemicolon, ";", start, line, col, false)
		return nil
	case c == '{':
		t.avancar()
		t.emitir(TokenBlockStart, "{", start, line, col, false)
		return nil
	case c == '}':
		t.avancar()
		t.emitir(TokenBlockEnd, "}", start, line, col, false)
		return nil
	case c == '#':
		for t.pos < len(t.src) && t.src[t.pos] != '\n' {
			t.avancar()
		}
		t.emitir(TokenComment, string(t.src[start+1:t.pos]), start, line, col, false)
		return nil
	case c == '"' || c == '\'':
		return t.lerAspas(c, start, line, col)
	default:
		return t.lerPalavra(start, line, col)
	}
}

func (t *tokenizer) lerAspas(aspa byte, start, line, col int) error {
	t.avancar() // consome a aspa de abertura

	var valor []byte
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		switch {
		case c == '\\' && t.pos+1 < len(t.src):
			t.avancar()
			valor = append(valor, t.src[t.pos])
			t.avancar()
		case c == aspa:
			t.avancar() // consome a aspa de fechamento
			t.emitir(TokenWord, string(valor), start, line, col, true)
			return nil
		default:
			valor = append(valor, c)
			t.avancar()
		}
	}
	return fmt.Errorf("aspa %q aberta na linha %d nao foi fechada", string(aspa), line)
}

func (t *tokenizer) lerPalavra(start, line, col int) error {
	var valor []byte
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		if ehEspaco(c) || c == ';' || c == '{' || c == '}' {
			break
		}
		if c == '\\' && t.pos+1 < len(t.src) {
			valor = append(valor, c)
			t.avancar()
			valor = append(valor, t.src[t.pos])
			t.avancar()
			continue
		}
		valor = append(valor, c)
		t.avancar()
	}
	t.emitir(TokenWord, string(valor), start, line, col, false)
	return nil
}

func (t *tokenizer) emitir(kind TokenKind, valor string, start, line, col int, quoted bool) {
	t.tokens = append(t.tokens, Token{
		Kind:   kind,
		Value:  valor,
		Raw:    string(t.src[start:t.pos]),
		Start:  start,
		End:    t.pos,
		Line:   line,
		Column: col,
		Quoted: quoted,
	})
}

func ehEspaco(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
