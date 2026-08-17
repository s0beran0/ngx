package config

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

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

// Token e um lexema com sua posicao exata. Value e o conteudo semantico (sem
// aspas, sem o # do comentario); Raw e o texto original.
//
// Start e End sao SEMPRE offsets em bytes: src[Start:End] == Raw, sempre,
// sem excecao -- isso e o que sustenta a edicao cirurgica da v0.2. Line e
// Column, por outro lado, contam RUNES, nao bytes: existem para leitura
// humana e de agente como posicao visual (a mesma coisa que um editor de
// texto mostraria), e um caractere multibyte (ç, ã, é) conta como uma unica
// coluna. Para offsets exatos em bytes use Start/End; nunca derive offset de
// byte a partir de Column.
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
// respeitando aspas, escapes, expansao de parametro (${...}) e comentarios
// -- casando token a token com o lexer do nginx-go-crossplane, que e o que
// a Task 9 usa para alinhar contra a arvore semantica.
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

// runeAqui devolve a rune que comeca em t.pos e seu tamanho em bytes, sem
// avancar. utf8.DecodeRune ja devolve (RuneError, 1) para um byte invalido
// ou uma sequencia truncada -- o tokenizador nunca trava numa entrada
// malformada (o fuzz gera essas entradas de proposito), so avanca 1 byte
// por vez por ela, do mesmo jeito que bufio.ScanRunes faz no lexer do
// crossplane.
func (t *tokenizer) runeAqui() (rune, int) {
	if t.pos >= len(t.src) {
		return 0, 0
	}
	return utf8.DecodeRune(t.src[t.pos:])
}

// espacoAqui diz se a rune em t.pos e espaco em branco no sentido unicode
// completo -- inclui \v, \f e NBSP (U+00A0), nao so os quatro bytes ascii.
// E o mesmo conjunto que o crossplane usa via strings.TrimSpace.
func (t *tokenizer) espacoAqui() bool {
	r, _ := t.runeAqui()
	return unicode.IsSpace(r)
}

// avancar consome uma rune inteira (1 ou mais bytes) a partir de t.pos,
// atualizando posicao, linha e coluna. Column conta runes; pos continua em
// bytes.
func (t *tokenizer) avancar() {
	r, tam := t.runeAqui()
	if tam == 0 {
		return
	}
	t.pos += tam
	if r == '\n' {
		t.line++
		t.col = 1
	} else {
		t.col++
	}
}

// consumirParaValor consome a rune em t.pos e devolve os bytes que devem
// entrar em Value: para uma rune valida, os proprios bytes da fonte; para um
// byte UTF-8 invalido, a codificacao da rune de substituicao U+FFFD -- e o
// que bufio.ScanRunes (o scanner usado pelo lexer do crossplane) devolve
// para bytes invalidos, mesmo avancando so 1 byte na fonte de entrada. Sem
// isso, um byte invalido no meio de uma palavra faz o nosso Value discordar
// do valor do crossplane, quebrando a comparacao diferencial da Task 9.
func (t *tokenizer) consumirParaValor() []byte {
	r, tam := t.runeAqui()
	antes := t.pos
	t.avancar()
	if r == utf8.RuneError && tam == 1 {
		return []byte(string(utf8.RuneError))
	}
	return t.src[antes:t.pos]
}

// consumirEscape consome a barra invertida em t.pos e, logo depois dela,
// qualquer sequencia de \r -- cada um invisivel, igual ao crossplane -- ate
// achar a rune real que forma o par de escape com essa barra (ja com bytes
// invalidos substituidos por U+FFFD via consumirParaValor). Isso replica um
// comportamento genuino do crossplane: o estado "escape pendente" atravessa
// um \r solto e se funde com o PROXIMO caractere de verdade, esteja ele onde
// estiver -- nao com o \r em si. Se a fonte acabar antes de achar essa rune
// (so havia a barra e talvez alguns \r ate o fim do arquivo), o par nunca se
// forma: tudo o que foi consumido fica invisivel (nunca entra em Value, mas
// continua avancando a posicao, entao continua dentro do Raw do token que
// estiver sendo construido), e ok volta false.
func (t *tokenizer) consumirEscape() (proximo []byte, ok bool) {
	t.avancar() // a barra em si, sempre ascii de 1 byte
	for t.pos < len(t.src) && t.src[t.pos] == '\r' {
		t.avancar()
	}
	if t.pos >= len(t.src) {
		return nil, false
	}
	return t.consumirParaValor(), true
}

func (t *tokenizer) pularEspacos() {
	for t.pos < len(t.src) && t.espacoAqui() {
		t.avancar()
	}
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
		t.lerComentario(start, line, col)
		return nil
	case c == '"' || c == '\'':
		return t.lerAspas(c, start, line, col)
	default:
		return t.lerPalavra(start, line, col)
	}
}

// lerComentario consome um comentario at o fim da linha. O CR de um
// terminador CRLF fica de fora do span do token: pertence ao espaco em
// branco que vem depois, nao ao comentario. Assim a v0.2, ao reescrever
// esse comentario, nunca converte a quebra de linha de CRLF para LF -- uma
// mudanca fora do alvo que o projeto promete nunca fazer.
func (t *tokenizer) lerComentario(start, line, col int) {
	for t.pos < len(t.src) {
		if t.src[t.pos] == '\n' {
			break
		}
		if t.src[t.pos] == '\r' && t.pos+1 < len(t.src) && t.src[t.pos+1] == '\n' {
			break
		}
		t.avancar()
	}
	t.emitir(TokenComment, string(t.src[start+1:t.pos]), start, line, col, false)
}

// lerAspas consome uma string entre aspas simples ou duplas. Barra invertida
// so e removida quando precede a aspa ativa (o delimitador atual); qualquer
// outro escape fica literal em Value, igual ao crossplane -- por isso
// msg "a\nb"; produz Value a\nb (barra e n literais), nao uma quebra de
// linha real. Um \r solto nunca entra em Value, fica invisivel, igual ao
// crossplane -- mas continua avancando a posicao, entao continua dentro do
// Raw. Uma barra seguida de \r pula os \r (invisiveis) e forma o par de
// escape com a rune real que vier depois, via consumirEscape.
func (t *tokenizer) lerAspas(aspa byte, start, line, col int) error {
	t.avancar() // consome a aspa de abertura

	var valor []byte
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		switch {
		case c == '\\':
			if prox, ok := t.consumirEscape(); ok {
				if len(prox) == 1 && prox[0] == aspa {
					valor = append(valor, prox...) // so a aspa, sem a barra
				} else {
					valor = append(valor, '\\')
					valor = append(valor, prox...)
				}
			}
		case c == '\r':
			// CR solto fica invisivel -- nunca entra em Value.
			t.avancar()
		case c == aspa:
			t.avancar() // consome a aspa de fechamento
			t.emitir(TokenWord, string(valor), start, line, col, true)
			return nil
		default:
			valor = append(valor, t.consumirParaValor()...)
		}
	}
	return fmt.Errorf("aspa %q aberta na linha %d nao foi fechada", string(aspa), line)
}

// lerPalavra consome uma palavra nao-quotada: nome de diretiva ou argumento.
// Trata ${...} (expansao de parametro, comum em templates Docker/envsubst,
// njs, rewrite e set) como parte da mesma palavra -- sem esse tratamento,
// "{" e "}" fantasmas aparecem no meio da palavra e desalinham a Task 9
// contra a arvore do crossplane. Um \r solto fica invisivel no meio da
// palavra, igual ao crossplane: nao termina a palavra, nunca entra em Value
// -- so um \n de verdade termina. Uma barra pula qualquer \r que vier logo
// depois e forma o par de escape com a rune real seguinte, via
// consumirEscape; se a fonte acabar antes disso, a barra (e os \r) somem
// sem deixar conteudo, exatamente como o crossplane.
func (t *tokenizer) lerPalavra(start, line, col int) error {
	var valor []byte
	for t.pos < len(t.src) {
		if len(valor) > 0 && valor[len(valor)-1] == '$' && t.src[t.pos] == '{' {
			antes := t.pos
			t.avancar() // consome o '{' que abre a expansao
			valor = append(valor, t.src[antes:t.pos]...)
			t.lerVar(&valor)
			continue
		}

		c := t.src[t.pos]
		if c == '\\' {
			if prox, ok := t.consumirEscape(); ok {
				valor = append(valor, '\\')
				valor = append(valor, prox...)
			}
			continue
		}
		if c == '\r' {
			// espelha lerComentario: o CR de um terminador CRLF fica de fora
			// do span, pertence ao espaco em branco que vem depois, nao a
			// palavra. So um CR solto (sem \n em seguida) e invisivel e
			// consumido aqui.
			if t.pos+1 < len(t.src) && t.src[t.pos+1] == '\n' {
				break
			}
			t.avancar()
			continue
		}
		if t.espacoAqui() || c == ';' || c == '{' || c == '}' {
			break
		}

		valor = append(valor, t.consumirParaValor()...)
	}
	if len(valor) == 0 {
		// a unica coisa consumida foi uma barra (e talvez \r) engolida sem
		// nunca achar par: nao ha conteudo nenhum, e o crossplane tambem
		// nao produz token para ela.
		return nil
	}
	t.emitir(TokenWord, string(valor), start, line, col, false)
	return nil
}

// lerVar consome o corpo de uma expansao de parametro (${...}) depois do
// '{' de abertura ja ter sido incorporado a palavra por lerPalavra. Espelha
// o estado inVar do lexer do crossplane, byte a byte: a leitura para (volta
// ao modo palavra normal) na primeira '}' ou no primeiro espaco em branco
// nao escapado, e os dois ainda fazem parte da mesma palavra -- e um
// comportamento estranho (o proprio crossplane documenta como um bug, "does
// not terminate on token boundary"), mas e o que ele faz, e este
// tokenizador precisa casar token a token com ele, nao corrigi-lo. Uma barra
// escapando qualquer coisa (exceto '}') nunca conta como o espaco que
// termina a expansao, so uma barra escapando '}' termina, igual ao
// crossplane. Um \r solto fica invisivel, igual em lerAspas; uma barra pula
// qualquer \r antes de formar o par de escape, via consumirEscape.
func (t *tokenizer) lerVar(valor *[]byte) {
	for t.pos < len(t.src) {
		c := t.src[t.pos]

		if c == '\\' {
			if prox, ok := t.consumirEscape(); ok {
				*valor = append(*valor, '\\')
				*valor = append(*valor, prox...)
				if len(prox) == 1 && prox[0] == '}' {
					return
				}
			}
			continue
		}
		if c == '\r' {
			// mesmo tratamento de lerPalavra: o CR de um CRLF fica de fora
			// do span, nao entra na expansao.
			if t.pos+1 < len(t.src) && t.src[t.pos+1] == '\n' {
				return
			}
			t.avancar()
			continue
		}

		espaco := t.espacoAqui()
		*valor = append(*valor, t.consumirParaValor()...)
		if espaco || (*valor)[len(*valor)-1] == '}' {
			return
		}
	}
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
