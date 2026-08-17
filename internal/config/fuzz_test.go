package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	crossplane "github.com/nginxinc/nginx-go-crossplane"

	"github.com/s0beran0/ngx/internal/config"
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

// FuzzAlinhamento verifica propriedades do casamento token-arvore (Task 9)
// que podem de fato falhar num alinhamento incorreto. "Span dentro dos
// limites da fonte" nao esta entre elas de proposito: Tokenize ja garante
// isso sozinho (Task 8), e um alinhador que so copiasse [0,len(src)) para
// todo no passaria nesse teste sem alinhar nada. As quatro propriedades
// abaixo dependem de COMO os offsets sao distribuidos entre os nos, que e
// onde um bug de alinhamento realmente vive:
//
//  1. cobertura: todo byte nao-espaco de nivel raiz pertence ao Span de
//     algum no raiz;
//  2. contencao/nao-sobreposicao: o Span de um filho vive dentro do Span do
//     pai, e irmaos nao se sobrepoem;
//  3. HeadSpan e exatamente "nome + argumentos": retokenizar o texto do
//     HeadSpan produz 1+len(Args) TokenWord e nada mais;
//  4. terminador: Span de uma diretiva nao-comentario termina em ';' ou '}'.
func FuzzAlinhamento(f *testing.F) {
	f.Add("server { listen 80; }")
	f.Add("http { server { location / { proxy_pass http://a; } } }")
	f.Add("# c\nevents {}")
	f.Add(`add_header X-A "b; c";`)
	f.Add("upstream u {\n server a;\n server b;\n}")
	f.Add("map $a $b {\n default 0;\n # com\n}")
	f.Add("location ~ \\.php$ { proxy_pass http://a; }")
	f.Add("server_name a.com # prod\n  b.com;")
	f.Add("location /api # gw\n{ proxy_pass http://a; }")
	f.Add("http { server { if ( $a = b ) { return 404; } } }")
	// Sem estas duas seeds o fuzz nunca exercitava arvore multi-arquivo: um
	// include gerado pelo fuzzer nao casa com arquivo nenhum, entao tree.Files
	// tinha sempre um elemento so e o alinhamento por arquivo -- o que a Task
	// 12 introduziu -- ficava sem cobertura de propriedade.
	f.Add("include incluido.conf;")
	f.Add("http { include incluido.conf; }\n# depois\n")

	f.Fuzz(func(t *testing.T, s string) {
		dir := t.TempDir()
		p := filepath.Join(dir, "f.conf")
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Skip()
		}
		// O arquivo incluido e fixo: o que varia e o texto que o inclui.
		incluido := "server_name incluido.exemplo; # do include\nlisten 8080;\n"
		if err := os.WriteFile(filepath.Join(dir, "incluido.conf"), []byte(incluido), 0o644); err != nil {
			t.Skip()
		}

		// Nenhum recover aqui, de proposito: panic e falha do fuzz. Uma CLI
		// consumida por agente nao pode emitir stack trace, entao "config.Parse
		// nunca entra em panico" e propriedade, nao ruido a ser pulado.
		tree, err := config.Parse(config.ParseOptions{Path: p})
		if err != nil {
			// erro do nosso lado nao e "fora de escopo" por si so: pode ser
			// sobre-rejeicao, a classe de bug que este fuzz existe para
			// achar. So esta de fato fora de escopo se o crossplane tambem
			// recusar a mesma entrada.
			verificarNaoSobreRejeicao(t, p, err)
			return
		}

		for _, arquivo := range tree.Files {
			verificarCoberturaDeRaiz(t, arquivo)
			verificarContencaoENaoSobreposicao(t, arquivo.Source, arquivo.Nodes, nil)
			verificarHeadSpanEhNomeMaisArgumentos(t, arquivo)
			verificarTerminadorDoSpan(t, arquivo)
		}
	})
}

// soTemCR informa se o resto da fonte e so \r (ou nada).
func soTemCR(resto []byte) bool {
	for _, b := range resto {
		if b != '\r' {
			return false
		}
	}
	return true
}

// verificarNaoSobreRejeicao e a propriedade que sustenta esta rodada de
// conserto: antes dela, "if err != nil { return }" tratava todo erro do
// nosso Parse como entrada fora de escopo, o que descarta por construcao
// exatamente a classe de bug que o aligner tinha -- sobre-rejeicao de
// configuracao valida. Aqui o oraculo e o proprio crossplane, rodado com as
// mesmas opcoes que internal/config/parse.go usa (Parse, parse.go:43-51):
// se ele aceita a entrada (sem erro e com Status != "failed") e o nosso
// Parse a recusa, isso e falha real, nao entrada invalida.
func verificarNaoSobreRejeicao(t *testing.T, path string, nossoErro error) {
	var problemas config.ParseErrors
	if errors.As(nossoErro, &problemas) && len(problemas) > 0 && divergenciaConhecida(problemas[0]) {
		return
	}

	payload, err := parseNoOraculo(path)
	if err != nil {
		return // crossplane tambem recusou: entrada legitimamente fora de escopo
	}
	if payload == nil {
		return // crossplane entrou em panico: nao e aceitacao
	}
	if payload.Status != "ok" {
		return // crossplane aceitou o arquivo mas registrou erro de parse: idem
	}
	t.Fatalf("sobre-rejeicao: crossplane aceitou a entrada mas o ngx recusou: %v\narquivo: %s",
		nossoErro, path)
}

// parseNoOraculo roda o crossplane com as mesmas opcoes de parse.go:43-51.
// O recover nao e complacencia: uma entrada que derruba o parser da
// dependencia (prepareIfArgs, util.go:83) nao esta sendo "aceita" por ela, e
// tratar isso como aceitacao acusaria o ngx de sobre-rejeitar justamente
// quando ele evitou um crash.
func parseNoOraculo(path string) (payload *crossplane.Payload, err error) {
	defer func() {
		if r := recover(); r != nil {
			payload, err = nil, nil
		}
	}()
	return crossplane.Parse(path, &crossplane.ParseOptions{
		ParseComments:             true,
		CombineConfigs:            false,
		SingleFile:                false,
		SkipDirectiveArgsCheck:    true,
		SkipDirectiveContextCheck: true,
		ErrorOnUnknownDirectives:  false,
	})
}

// divergenciaConhecida e a lista FECHADA de recusas do ngx que o crossplane
// nao faz. Ela existe porque o oraculo tem que continuar acusando
// sobre-rejeicao: a versao anterior deste arquivo silenciava por substring da
// mensagem ("aspa", "token inesperado", "esperava", "sobraram"), o que
// apagava a classe inteira de bug que o fuzz existe para achar -- qualquer
// recusa nova do aligner cairia numa daquelas substrings.
//
// Cada entrada casa a CLASSE mais a forma exata do token, tem citacao do
// fonte do crossplane e tem teste unitario proprio em robustez_test.go. Uma
// recusa que nao esteja aqui -- inclusive uma nova recusa da mesma classe com
// outro token -- e falha do fuzz, como tem que ser. Classes deliberadamente
// FORA da lista: RecusaTokenInesperado, RecusaTokensSobrando,
// RecusaFimInesperado e RecusaPanicoDoCrossplane, que so aparecem quando o
// casamento entre arvore e tokens saiu do lugar -- isto e, quando ha bug.
func divergenciaConhecida(pe config.ParseError) bool {
	switch pe.Classe {
	case config.RecusaAspaNaoFechada:
		// lex.go:325-327 fecha a aspa implicitamente no fim do arquivo e nao
		// emite token nenhum se o conteudo estiver vazio: uma aspa solta e
		// "ok" para o crossplane. O nginx recusa. Ver
		// TestDivergenciaAspaNaoFechada.
		return pe.Token == `"` || pe.Token == "'"

	case config.RecusaTokenNoLugarDeDiretiva:
		// parse.go:256-261 monta o statement com t.Value sem exigir que o
		// primeiro token seja uma palavra: so "}" (parse.go:237) e comentario
		// (parse.go:264) sao tratados a parte, entao "{", "}" e ";" viram
		// nome de diretiva para ele. Esses tres sao TODOS os tokens que nao
		// sao palavra nem comentario -- a lista e exaustiva sobre os Kind do
		// tokenizador, e uma palavra recusada nessa posicao continua sendo
		// bug. Ver TestDivergenciaChaveComoNomeDeDiretiva e
		// TestDivergenciaPontoEVirgulaComoNomeDeDiretiva.
		return pe.Token == "{" || pe.Token == "}" || pe.Token == ";"

	case config.RecusaTerminadorAusente:
		// O laco de argumentos para em "}" (parse.go:285) e a checagem
		// "is not terminated by \";\"" (analyze.go:224-227) nao roda com
		// SkipDirectiveArgsCheck (analyze.go:202-204). So o "}" diverge. Ver
		// TestDivergenciaDiretivaSemPontoEVirgula.
		return pe.Token == "}"

	case config.RecusaExpressaoIfInvalida:
		// Guarda validExpr (analyze.go:212, util.go:57-67) que
		// SkipDirectiveArgsCheck suprime e sem a qual prepareIfArgs
		// (util.go:83) derruba o processo. O token e sempre o nome "if",
		// citado ou nao (parse.go:352-354 compara sem olhar IsQuoted). Ver
		// TestIfComExpressaoVaziaEhRecusaTipadaENaoPanic.
		return pe.Token == "if" || pe.Token == `"if"` || pe.Token == "'if'"
	}
	return false
}

// verificarCoberturaDeRaiz confere que nenhum byte significativo de nivel
// raiz escapa do Span de todo no raiz -- a formulacao concreta de que o
// casamento nao "perdeu" nenhum trecho do documento.
//
// "Significativo" usa a mesma nocao de espaco que o tokenizador (unicode.
// IsSpace, decodificado rune a rune) -- nao so os quatro bytes ascii. Uma
// primeira versao deste helper checava so ' ', '\t', '\n', '\r' e o fuzz
// achou "\v" (vertical tab) como falso positivo em minutos: o tokenizador
// corretamente trata \v como espaco (tokens.go, espacoAqui) e nao emite
// token nenhum para ele, entao ele fica fora de qualquer span de proposito
// -- o defeito era no teste, nao no alinhamento.
//
// Uma barra invertida sozinha (sem par de escape, tipicamente no ultimo
// byte do arquivo) e o mesmo gap legitimo documentado em verificarCobertura
// no fuzz do tokenizador: consumida pelo tokenizador (avanca a posicao) mas
// sem formar token nenhum, entao nao e espaco e nao esta em span nenhum --
// o fuzz achou esse caso tambem, na mesma rodada.
func verificarCoberturaDeRaiz(t *testing.T, arquivo *config.File) {
	src := arquivo.Source
	coberto := make([]bool, len(src))
	for _, n := range arquivo.Nodes {
		for i := n.Span.Start; i < n.Span.End; i++ {
			coberto[i] = true
		}
	}
	for i := 0; i < len(src); {
		if coberto[i] {
			i++
			continue
		}
		// A valvula da barra invertida so vale para a barra SEM par de escape,
		// que consumirEscape (tokens.go:134-143) so devolve como ok == false
		// no fim da fonte -- \r e invisivel e nao conta como par. Antes ela
		// pulava qualquer '\' fora de span, o que tambem perdoaria uma barra
		// no meio do arquivo deixada de fora por outro motivo.
		if src[i] == '\\' && soTemCR(src[i+1:]) {
			i++
			continue
		}
		r, tam := utf8.DecodeRune(src[i:])
		if tam == 0 {
			tam = 1
		}
		if !unicode.IsSpace(r) {
			t.Fatalf("byte %d (%q) fora de qualquer span de nivel raiz e nao e espaco", i, string(src[i]))
		}
		i += tam
	}
}

// verificarContencaoENaoSobreposicao confere, recursivamente, que o Span de
// cada filho vive dentro do Span do pai e que irmaos nao se sobrepoem --
// sem essa propriedade, uma reescrita por substituicao de bytes na v0.2
// corromperia o arquivo.
//
// Excecao deliberada para nao-comentario vs comentario: um comentario
// encontrado no meio dos argumentos de uma diretiva (Task 9, defeito 1)
// chega aqui como IRMAO da diretiva anterior, mas seu texto fica
// fisicamente DENTRO do span dela -- e assim que o proprio crossplane
// estrutura a arvore (parse.go:286-290 poe o comentario fora de Args,
// parse.go:435-445 o anexa como no "#" depois da diretiva e do bloco dela),
// nao um defeito de alinhamento. Por isso a checagem de nao-sobreposicao
// contra o irmao anterior so vale para nos que nao sao comentario.
func verificarContencaoENaoSobreposicao(t *testing.T, src []byte, nodes []*config.Node, pai *config.Node) {
	anteriorFim := -1
	for _, n := range nodes {
		if n.Span.Start < 0 || n.Span.End > len(src) || n.Span.Start > n.Span.End {
			t.Fatalf("span invalido [%d,%d) para %q em fonte de %d bytes",
				n.Span.Start, n.Span.End, n.Directive, len(src))
		}
		if pai != nil {
			if n.Span.Start < pai.Span.Start || n.Span.End > pai.Span.End {
				t.Fatalf("span de %q [%d,%d) nao esta contido no do pai %q [%d,%d)",
					n.Directive, n.Span.Start, n.Span.End, pai.Directive, pai.Span.Start, pai.Span.End)
			}
		}
		if !n.IsComment() && n.Span.Start < anteriorFim {
			t.Fatalf("span de %q comeca em %d, antes do fim do irmao anterior em %d",
				n.Directive, n.Span.Start, anteriorFim)
		}
		if n.Span.End > anteriorFim {
			anteriorFim = n.Span.End
		}
		verificarContencaoENaoSobreposicao(t, src, n.Block, n)
	}
}

// verificarHeadSpanEhNomeMaisArgumentos confere que o HeadSpan cobre
// exatamente o nome da diretiva e seus argumentos, nada mais e nada menos:
// retokenizar o texto do HeadSpan tem que produzir so TokenWord (e, desde a
// Task 9 defeito 1, TokenComment tambem — um comentario no meio dos
// argumentos fica fisicamente dentro do HeadSpan, ver align.go) e nenhum
// outro tipo de token. Um alinhador que incluisse o proximo token (';' ou
// '{') seria pego aqui de qualquer forma, comentario ou nao.
//
// A contagem exata "1 diretiva + len(Args) palavras" vale para toda
// diretiva, exceto "if": prepareIfArgs (crossplane/util.go:71-86) remove de
// Args os tokens "(" e ")" quando vem isolados, entao len(n.Args) nao conta
// os tokens-palavra reais entre o nome e o terminador (Task 9, defeito 2).
// Para "if" a checagem de tipo de token acima (nada alem de palavra ou
// comentario) e o que pega um alinhador que avancasse demais.
func verificarHeadSpanEhNomeMaisArgumentos(t *testing.T, arquivo *config.File) {
	var percorrer func(nodes []*config.Node)
	percorrer = func(nodes []*config.Node) {
		for _, n := range nodes {
			if n.IsComment() {
				percorrer(n.Block)
				continue
			}
			if n.HeadSpan.Start < n.Span.Start || n.HeadSpan.End > n.Span.End {
				t.Fatalf("head span de %q [%d,%d) fora do span do no [%d,%d)",
					n.Directive, n.HeadSpan.Start, n.HeadSpan.End, n.Span.Start, n.Span.End)
			}

			texto := string(arquivo.Source[n.HeadSpan.Start:n.HeadSpan.End])
			toks, err := config.Tokenize([]byte(texto))
			if err != nil {
				t.Fatalf("head span de %q nao retokeniza (%v); texto=%q", n.Directive, err, texto)
			}

			var palavras int
			for _, tk := range toks {
				if tk.Kind == config.TokenComment {
					continue
				}
				if tk.Kind != config.TokenWord {
					t.Fatalf("head span de %q contem token %v que nao e palavra nem comentario; texto=%q",
						n.Directive, tk.Kind, texto)
				}
				palavras++
			}
			if n.Directive != "if" {
				if esperado := 1 + len(n.Args); palavras != esperado {
					t.Fatalf("head span de %q tem %d palavras, esperava %d (1 diretiva + %d args); texto=%q",
						n.Directive, palavras, esperado, len(n.Args), texto)
				}
			}

			percorrer(n.Block)
		}
	}
	percorrer(arquivo.Nodes)
}

// verificarTerminadorDoSpan confere que o Span de toda diretiva
// nao-comentario termina no delimitador esperado -- ';' para diretiva
// simples, '}' para bloco. Um alinhador que parasse um token antes ou
// depois do delimitador real seria pego aqui.
func verificarTerminadorDoSpan(t *testing.T, arquivo *config.File) {
	src := arquivo.Source
	var percorrer func(nodes []*config.Node)
	percorrer = func(nodes []*config.Node) {
		for _, n := range nodes {
			if !n.IsComment() {
				if n.Span.End < 1 || n.Span.End > len(src) {
					t.Fatalf("span fim invalido para %q: %d (fonte tem %d bytes)",
						n.Directive, n.Span.End, len(src))
				}
				ultimo := src[n.Span.End-1]
				if ultimo != ';' && ultimo != '}' {
					t.Fatalf("span de %q termina em %q, esperava ';' ou '}'", n.Directive, string(ultimo))
				}
			}
			percorrer(n.Block)
		}
	}
	percorrer(arquivo.Nodes)
}
