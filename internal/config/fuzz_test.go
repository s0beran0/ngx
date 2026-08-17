package config_test

import (
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	crossplane "github.com/nginxinc/nginx-go-crossplane"

	"github.com/eduardoborges/ngx/internal/config"
)

// O fuzz garante que, para qualquer entrada que o tokenizador aceite, os
// spans continuam apontando para o texto real e em ordem crescente, todo
// byte fica coberto por algum token ou e espaco, Kind e Raw sao coerentes,
// a coluna reconstruida a partir do texto bate com a coluna reportada, o
// resultado e o mesmo em duas passagens, e -- a propriedade que realmente
// sustenta a Task 9 -- os tokens casam com os do lexer do
// nginx-go-crossplane, contagem e valor, ignorando comentarios.
func FuzzTokenizeSpans(f *testing.F) {
	f.Add("server { listen 80; }")
	f.Add(`add_header X "a; b";`)
	f.Add("# comentario\nhttp { }")
	f.Add(`location ~ \.php$ { }`)
	f.Add("map $a $b {\n default 0;\n}")
	f.Add(`proxy_pass http://${backend};`)
	f.Add("set $a \"${b}c\";")
	f.Add("listen 80;\r\nserver_name a;\r\n# fim\r\n")
	f.Add("proxy_set_header Host\r\n  $host;\r\n")
	f.Add(`log_format m '$remote_addr "$http_user_agent"';`)
	f.Add(`msg "diz \"oi\" e \\ e \n";`)
	f.Add("map $a $b {\n  ~^/x  \"y; z\";\n  default 0;\n}")
	f.Add(`location ~ "^/a{2,3}\.php$" { }`)
	f.Add("# comentario ç\nserver_name exemplo.com.br;")
	f.Add("proxy_pass http://\"host\";")
	f.Add("foo \\")

	f.Fuzz(func(t *testing.T, s string) {
		toks, err := config.Tokenize([]byte(s))
		if err != nil {
			return // entrada fora de escopo: o nosso tokenizador a recusou
		}

		verificarSpansEOrdem(t, s, toks)
		verificarCobertura(t, s, toks)
		verificarCoerenciaKindRaw(t, toks)
		verificarLinhaEColuna(t, s, toks)
		verificarIdempotencia(t, s, toks)
		verificarDiferencialContraCrossplane(t, s, toks)
		verificarCRLFNuncaTerminaSpan(t, s, toks)
	})
}

// verificarCRLFNuncaTerminaSpan e a propriedade que sustenta a correcao do
// CR de CRLF em lerPalavra e lerVar (fix round 2): nenhum token pode
// terminar num \r que seja seguido de \n na fonte. Esse CR pertence ao
// espaco em branco depois do token, nunca ao span do token -- senao uma
// reescrita por substituicao de bytes converteria a linha de CRLF para LF.
func verificarCRLFNuncaTerminaSpan(t *testing.T, s string, toks []config.Token) {
	for _, tok := range toks {
		if tok.End == 0 || s[tok.End-1] != '\r' {
			continue
		}
		if tok.End < len(s) && s[tok.End] == '\n' {
			t.Fatalf("token %q em [%d,%d) termina num \\r seguido de \\n na fonte %q",
				tok.Value, tok.Start, tok.End, s)
		}
	}
}

// verificarSpansEOrdem confere a higiene basica dos spans: ordem crescente,
// limites dentro da fonte e Raw == fatia da fonte.
func verificarSpansEOrdem(t *testing.T, s string, toks []config.Token) {
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
}

// verificarCobertura confere que todo byte fora de algum span e espaco em
// branco (decodificando rune a rune, para nao confundir byte de continuacao
// UTF-8 com "nao e espaco").
func verificarCobertura(t *testing.T, s string, toks []config.Token) {
	coberto := make([]bool, len(s))
	for _, tok := range toks {
		for i := tok.Start; i < tok.End; i++ {
			coberto[i] = true
		}
	}

	for i := 0; i < len(s); {
		if coberto[i] {
			i++
			continue
		}
		// uma barra invertida pode ficar de fora de qualquer token: sozinha
		// no ultimo byte da fonte (sem caractere seguinte para formar par de
		// escape), ou junto com o \r que a segue quando os dois formam a
		// palavra inteira (o crossplane tambem nao produz token nenhum
		// nesse caso -- ver barraFinalDeArquivo e o tratamento de \\+\r em
		// tokens.go). Nao e espaco, mas e um gap legitimo, do mesmo jeito
		// que o proprio crossplane deixa.
		if s[i] == '\\' {
			i++
			continue
		}
		r, tam := utf8.DecodeRuneInString(s[i:])
		if tam == 0 {
			tam = 1
		}
		if !unicode.IsSpace(r) {
			t.Fatalf("byte %d (%q) nao coberto por nenhum token e nao e espaco", i, s[i])
		}
		i += tam
	}
}

// verificarCoerenciaKindRaw confere que Kind e Raw (e Value, para palavras
// nao-quotadas) nunca se contradizem.
func verificarCoerenciaKindRaw(t *testing.T, toks []config.Token) {
	for _, tok := range toks {
		switch tok.Kind {
		case config.TokenSemicolon:
			if tok.Raw != ";" {
				t.Fatalf("TokenSemicolon com raw %q", tok.Raw)
			}
		case config.TokenBlockStart:
			if tok.Raw != "{" {
				t.Fatalf("TokenBlockStart com raw %q", tok.Raw)
			}
		case config.TokenBlockEnd:
			if tok.Raw != "}" {
				t.Fatalf("TokenBlockEnd com raw %q", tok.Raw)
			}
		case config.TokenComment:
			if len(tok.Raw) == 0 || tok.Raw[0] != '#' {
				t.Fatalf("TokenComment com raw %q sem # inicial", tok.Raw)
			}
		case config.TokenWord:
			if tok.Quoted {
				continue
			}
			esperado := valorEsperadoParaPalavra(tok.Raw)
			if tok.Value != esperado {
				t.Fatalf("TokenWord nao-quotado com value %q != esperado %q (raw %q)",
					tok.Value, esperado, tok.Raw)
			}
		}
	}
}

// valorEsperadoParaPalavra recalcula, a partir do Raw de um TokenWord
// nao-quotado, o Value que a producao deveria ter gerado -- espelhando
// consumirEscape em tokens.go: uma barra pula qualquer \r que vier logo
// depois (invisivel) e forma o par de escape com a rune real seguinte
// (literal, os dois bytes, com o byte invalido substituido por U+FFFD); se
// a fonte acabar antes de achar essa rune, a barra e os \r somem sem deixar
// conteudo. Um \r solto (fora de um par de escape) tambem fica invisivel.
func valorEsperadoParaPalavra(raw string) string {
	var saida strings.Builder
	i := 0
	for i < len(raw) {
		if raw[i] == '\\' {
			j := i + 1
			for j < len(raw) && raw[j] == '\r' {
				j++
			}
			if j >= len(raw) {
				// nunca achou a rune do par: barra e \r somem sem deixar
				// conteudo (isso so pode acontecer no fim absoluto do
				// arquivo, que e onde a fonte de fato acaba).
				return saida.String()
			}
			saida.WriteByte('\\')
			r, tam := utf8.DecodeRuneInString(raw[j:])
			if r == utf8.RuneError && tam == 1 {
				saida.WriteRune(utf8.RuneError)
			} else {
				saida.WriteString(raw[j : j+tam])
			}
			i = j + tam
			continue
		}
		if raw[i] == '\r' {
			i++
			continue
		}
		r, tam := utf8.DecodeRuneInString(raw[i:])
		if r == utf8.RuneError && tam == 1 {
			saida.WriteRune(utf8.RuneError)
			i++
			continue
		}
		saida.WriteString(raw[i : i+tam])
		i += tam
	}
	return saida.String()
}

// verificarLinhaEColuna reconstroi linha e coluna a partir do texto e
// compara com o que cada token reportou. Coluna conta runes, nao bytes.
//
// Faz isso num unico passe O(n) pela fonte, avancando um cursor byte a byte
// (nunca reescaneando do inicio da linha ou do arquivo a cada token) --
// tokens vem em ordem crescente de Start, entao o cursor so precisa andar
// para frente. A primeira versao deste helper reescaneava o prefixo inteiro
// a cada token (O(n) por token, O(n^2) no total) e um fuzz de 60s achava
// uma entrada com muitos tokens numa linha so que travava o processo ate o
// timeout do proprio go test -fuzz.
func verificarLinhaEColuna(t *testing.T, s string, toks []config.Token) {
	pos, linha, coluna := 0, 1, 1
	for _, tok := range toks {
		for pos < tok.Start {
			r, tam := utf8.DecodeRuneInString(s[pos:])
			if tam == 0 {
				tam = 1
			}
			pos += tam
			if r == '\n' {
				linha++
				coluna = 1
			} else {
				coluna++
			}
		}
		if tok.Line != linha {
			t.Fatalf("linha %d != esperada %d para token %q em %d", tok.Line, linha, tok.Value, tok.Start)
		}
		if tok.Column != coluna {
			t.Fatalf("coluna %d != esperada %d para token %q em %d", tok.Column, coluna, tok.Value, tok.Start)
		}
	}
}

// verificarIdempotencia confere que tokenizar a mesma fonte duas vezes
// produz exatamente o mesmo resultado.
func verificarIdempotencia(t *testing.T, s string, toks []config.Token) {
	outra, err := config.Tokenize([]byte(s))
	if err != nil {
		t.Fatalf("tokenizar de novo produziu erro: %v", err)
	}
	if !reflect.DeepEqual(toks, outra) {
		t.Fatalf("tokenizar duas vezes produziu resultados diferentes:\nprimeira: %+v\nsegunda:  %+v", toks, outra)
	}
}

// verificarDiferencialContraCrossplane e a propriedade que sustenta a Task
// 9: o aligner casa nossos tokens com os do crossplane por contagem e tipo,
// sem nunca comparar valores -- entao qualquer divergencia aqui e uma
// divergencia real de alinhamento. Se o crossplane rejeitar a entrada (erro
// em algum token), ela esta fora de escopo e a comparacao e pulada.
func verificarDiferencialContraCrossplane(t *testing.T, s string, toks []config.Token) {
	ch := crossplane.Lex(strings.NewReader(s))

	var referencia []crossplane.NgxToken
	for tok := range ch {
		if tok.Error != nil {
			// drena o resto do canal para nao deixar a goroutine do
			// crossplane vazando, e sai: entrada fora de escopo.
			for range ch {
			}
			return
		}
		referencia = append(referencia, tok)
	}

	var nossos []config.Token
	for _, tok := range toks {
		if tok.Kind == config.TokenComment {
			continue
		}
		nossos = append(nossos, tok)
	}

	var deles []crossplane.NgxToken
	for _, tok := range referencia {
		if !tok.IsQuoted && strings.HasPrefix(tok.Value, "#") {
			continue
		}
		deles = append(deles, tok)
	}

	if len(nossos) != len(deles) {
		t.Fatalf("contagem de tokens diverge do crossplane para %q: nosso=%d crossplane=%d\nnosso=%v\ncrossplane=%v",
			s, len(nossos), len(deles), nossos, deles)
	}
	for i := range nossos {
		if nossos[i].Value != deles[i].Value {
			t.Fatalf("token %d diverge do crossplane para %q: nosso=%q crossplane=%q",
				i, s, nossos[i].Value, deles[i].Value)
		}
		if nossos[i].Quoted != deles[i].IsQuoted {
			t.Fatalf("token %d diverge do crossplane em Quoted para %q: nosso=%v crossplane=%v (valor %q)",
				i, s, nossos[i].Quoted, deles[i].IsQuoted, nossos[i].Value)
		}
	}
}
