package config

import (
	"fmt"
	"strings"
)

// Por que SkipDirectiveArgsCheck esta ligado em Parse (parse.go), e por que
// esta validacao existe apesar disso:
//
// A checagem de argumentos do crossplane (analyze.go:206-247) roda contra uma
// tabela de diretivas gerada para uma versao especifica do nginx e dos modulos
// dela. Diretiva desconhecida ja e ignorada antes disso -- analyze.go:176-178
// devolve nil quando !knownDirective, independente da flag --, entao a flag
// NAO existe para aceitar diretiva de terceiro: ela existe para nao recusar
// uma diretiva CONHECIDA cuja aridade mudou entre versoes do nginx ou do
// modulo. Um ngx que recusa um .conf que o nginx do usuario aceita e pior que
// um ngx que aceita algo que o nginx recusaria: quem valida semantica e o
// "nginx -t", e o ngx e uma ferramenta de leitura e edicao.
//
// So que uma das guardas suprimidas nao era de aridade: analyze.go:212
// (`(mask&ngxConfExpr) > 0 && !validExpr(stmt)`) e o unico ponto que impede o
// crossplane de entregar um "if" mal formado para prepareIfArgs
// (util.go:71-86), que para Args == ["()"] faz d.Args[1:0] e derruba o
// processo (util.go:83). Esta funcao recoloca exatamente essa guarda, so para
// "if", replicando validExpr (util.go:57-67) argumento a argumento -- nada de
// aridade, nada de contexto.
//
// Rodar antes de entregar o arquivo ao crossplane e o que torna isso uma
// correcao de causa raiz e nao um paliativo: o panic deixa de acontecer, em
// vez de ser capturado depois. A barreira de recover em parse.go continua
// existindo para a proxima surpresa da dependencia, nao para esta.

// validarExpressoesIf devolve as recusas das diretivas "if" cuja expressao
// nao esta entre parenteses. Trabalha sobre os tokens deste pacote, que
// casam token a token com o lexer do crossplane.
//
// Fonte que nao tokeniza devolve zero recusas de proposito: ali quem decide
// e o alinhador (que classifica a recusa) ou o proprio crossplane, e um
// palpite sobre tokens que nao existem so produziria mensagem errada.
func validarExpressoesIf(path string, src []byte) ParseErrors {
	toks, err := Tokenize(src)
	if err != nil {
		return nil
	}

	var problemas ParseErrors
	// mapLike conta os blocos map-like abertos. Dentro deles o crossplane
	// nem chega em analyze/prepareIfArgs: parse.go:304-321 anexa o statement
	// e faz continue. Um "if" ali e parametro de map, nao diretiva -- recusar
	// seria sobre-rejeicao.
	mapLike := 0
	i := 0
	for i < len(toks) {
		t := toks[i]
		switch t.Kind {
		case TokenComment, TokenSemicolon:
			i++
			continue
		case TokenBlockEnd:
			if mapLike > 0 {
				mapLike--
			}
			i++
			continue
		case TokenBlockStart:
			// "{" onde se esperava um nome de diretiva. O crossplane o trata
			// como nome (parse.go:256-261); aqui so precisamos manter a
			// contagem de blocos coerente.
			i++
			continue
		}

		nome := t
		i++
		var args []string
		for i < len(toks) {
			k := toks[i].Kind
			if k == TokenSemicolon || k == TokenBlockStart || k == TokenBlockEnd {
				break
			}
			if k == TokenWord {
				args = append(args, toks[i].Value)
			}
			// TokenComment no meio dos argumentos nao entra em Args
			// (crossplane/parse.go:286-290).
			i++
		}

		abreBloco := i < len(toks) && toks[i].Kind == TokenBlockStart
		if abreBloco {
			if ehCorpoMapLike(nome.Value) || mapLike > 0 {
				mapLike++
			}
			i++
		} else if i < len(toks) && toks[i].Kind == TokenSemicolon {
			i++
		}

		// stmt.Directive e o VALOR do token, com ou sem aspas: o crossplane
		// compara `stmt.Directive == "if"` sem olhar IsQuoted
		// (parse.go:352-354), entao `"if" ()` cai no mesmo prepareIfArgs.
		if mapLike > 0 {
			continue
		}
		if nome.Value != "if" {
			continue
		}
		if expressaoValida(args) {
			continue
		}
		problemas = append(problemas, ParseError{
			File:    path,
			Line:    nome.Line,
			Message: fmt.Sprintf("diretiva \"if\" com expressao %q: a expressao precisa estar entre parenteses e nao pode ser vazia", strings.Join(args, " ")),
			Classe:  RecusaExpressaoIfInvalida,
			Token:   nome.Raw,
		})
	}
	return problemas
}

// expressaoValida replica validExpr (crossplane/util.go:57-67) sobre os
// valores dos argumentos: primeiro argumento comecando em "(", ultimo
// terminando em ")", e a expressao entre eles nao pode ser vazia -- e a
// vacuidade e testada pelo TAMANHO dos tokens de borda, exatamente como no
// original, porque e isso que prepareIfArgs (util.go:71-86) assume.
func expressaoValida(args []string) bool {
	l := len(args)
	if l == 0 {
		return false
	}
	b, e := 0, l-1
	if !strings.HasPrefix(args[b], "(") || !strings.HasSuffix(args[e], ")") {
		return false
	}
	switch {
	case l == 1:
		return len(args[b]) > 2
	case l == 2:
		return len(args[b]) > 1 || len(args[e]) > 1
	default:
		return true
	}
}
