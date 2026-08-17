package config

import (
	"errors"
	"fmt"
)

// alinhar casa a arvore semantica vinda do crossplane com os tokens do
// arquivo, anexando offsets de byte a cada no.
//
// O casamento e por sequencia, mas "o crossplane preserva a ordem do
// documento" e falso para um caso: um comentario encontrado no meio dos
// argumentos de uma diretiva (crossplane/parse.go:286-290, commentsInArgs)
// nao entra em Args e nao fica onde apareceu no texto -- ele e anexado como
// no "#" irmao DEPOIS da diretiva inteira, e depois do bloco dela se houver
// (parse.go:435-445). O aligner drena esses tokens de comentario do meio dos
// argumentos e os casa, em ordem, com esses nos "#" que aparecem depois --
// ver drenarComentarios e o parametro pendentes.
//
// Dentro de um corpo map-like esse reanexo NAO acontece: o statement e
// anexado em parse.go:318 e o laco faz continue em parse.go:319, antes do
// laco de commentsInArgs de parse.go:436, entao os comentarios do meio dos
// argumentos sao descartados pelo crossplane. Ali a fila precisa ser
// descartada tambem -- ver ehCorpoMapLike.
func alinhar(f *File) error {
	toks, err := Tokenize(f.Source)
	if err != nil {
		var aspa *ErroDeAspa
		if errors.As(err, &aspa) {
			return ParseErrors{{
				File:    f.Path,
				Line:    aspa.Linha,
				Message: aspa.Error(),
				Classe:  RecusaAspaNaoFechada,
				Token:   aspa.Aspa,
			}}
		}
		return fmt.Errorf("ao tokenizar %s: %w", f.Path, err)
	}

	a := &aligner{file: f.Path, toks: toks}
	if err := a.nos(f.Nodes, false); err != nil {
		return err
	}
	if a.pos != len(a.toks) {
		sobrou := a.toks[a.pos]
		return ParseErrors{{
			File:    f.Path,
			Line:    sobrou.Line,
			Message: fmt.Sprintf("sobraram %d tokens apos alinhar a arvore", len(a.toks)-a.pos),
			Classe:  RecusaTokensSobrando,
			Token:   sobrou.Raw,
		}}
	}
	return nil
}

type aligner struct {
	file string
	toks []Token
	pos  int
}

// mapBodies replica, por nome, a tabela de mesmo nome do crossplane
// (analyze_map.go:20-46). Um bloco desses nao tem seu corpo analisado como
// diretiva: parse.go:304-321 anexa o statement e segue.
var corposMapLike = map[string]bool{
	"charset_map":   true,
	"geo":           true,
	"map":           true,
	"match":         true,
	"types":         true,
	"split_clients": true,
	"geoip2":        true,
	"otel_exporter": true,
}

func ehCorpoMapLike(diretiva string) bool { return corposMapLike[diretiva] }

func (a *aligner) nos(nodes []*Node, corpoMapLike bool) error {
	// pendentes e a fila de tokens de comentario drenados do meio dos
	// argumentos de algum no deste MESMO nivel (irmaos), na ordem em que
	// apareceram no texto. E local a esta chamada de nos() -- e nao um campo
	// do aligner -- porque cada nivel de bloco tem sua propria sequencia de
	// nos "#" apos as diretivas; se fosse compartilhada entre niveis, um
	// comentario dos argumentos de um no com bloco furaria a ordem quando o
	// bloco tivesse, por sua vez, diretivas com comentario nos argumentos.
	//
	// fila == nil marca um corpo map-like: ali o crossplane descarta os
	// comentarios do meio dos argumentos em vez de reanexa-los, entao nao ha
	// no "#" nenhum para reclamar a fila. Enfileirar mesmo assim faria o
	// proximo comentario avulso do mesmo nivel casar com o token errado --
	// e recusaria uma config valida.
	var pendentes []Token
	fila := &pendentes
	if corpoMapLike {
		fila = nil
	}
	for _, n := range nodes {
		if err := a.no(n, fila); err != nil {
			return err
		}
	}
	return nil
}

func (a *aligner) no(n *Node, pendentes *[]Token) error {
	if n.IsComment() {
		if pendentes != nil && len(*pendentes) > 0 {
			tok := (*pendentes)[0]
			*pendentes = (*pendentes)[1:]
			n.Line, n.Column = tok.Line, tok.Column
			n.Span = Span{tok.Start, tok.End}
			n.HeadSpan = n.Span
			return nil
		}
		tok, err := a.consumir(TokenComment)
		if err != nil {
			return err
		}
		n.Line, n.Column = tok.Line, tok.Column
		n.Span = Span{tok.Start, tok.End}
		n.HeadSpan = n.Span
		return nil
	}

	nome, err := a.consumirNomeDeDiretiva()
	if err != nil {
		return err
	}
	n.Line, n.Column = nome.Line, nome.Column

	fimDaCabeca := nome.End
	var comentariosDaCabeca []Token
	a.drenarComentarios(pendentes, &comentariosDaCabeca)

	if n.Directive == "if" {
		// prepareIfArgs (crossplane/util.go:71-86) remove de Args os tokens
		// "(" e ")" quando eles vem isolados (com espaco em volta), entao
		// len(n.Args) nao conta os tokens-palavra reais entre o nome e o
		// terminador -- ver defeito 2 da Task 9. Consome por posicao do
		// terminador, nao por contagem de Args.
		for {
			proximo, err := a.espiar()
			if err != nil {
				return err
			}
			// TokenBlockEnd tambem encerra a coleta: o laco de argumentos do
			// crossplane para em "}" (parse.go:285), entao "x { if (a) }" e
			// um "if" sem terminador, nao um "if" com o "}" de argumento.
			// Parar aqui e o que faz a recusa sair classificada como
			// RecusaTerminadorAusente -- divergencia conhecida -- em vez de
			// token inesperado, que e a classe de bug do aligner.
			if proximo.Kind == TokenSemicolon || proximo.Kind == TokenBlockStart ||
				proximo.Kind == TokenBlockEnd {
				break
			}
			arg, err := a.consumir(TokenWord)
			if err != nil {
				return err
			}
			fimDaCabeca = arg.End
			a.drenarComentarios(pendentes, &comentariosDaCabeca)
		}
	} else {
		for range n.Args {
			arg, err := a.consumir(TokenWord)
			if err != nil {
				return err
			}
			fimDaCabeca = arg.End
			a.drenarComentarios(pendentes, &comentariosDaCabeca)
		}
	}
	n.HeadSpan = Span{nome.Start, fimDaCabeca}
	n.HeadComments = comentariosDentro(comentariosDaCabeca, n.HeadSpan)

	// Olhar o proximo token e mais confiavel que inspecionar n.Block: um
	// bloco vazio e indistinguivel de uma diretiva simples pelo campo Block.
	proximo, err := a.espiar()
	if err != nil {
		return err
	}

	switch proximo.Kind {
	case TokenSemicolon:
		fim, _ := a.consumir(TokenSemicolon)
		n.temBloco = false
		n.Span = Span{nome.Start, fim.End}
		return nil

	case TokenBlockStart:
		if _, err := a.consumir(TokenBlockStart); err != nil {
			return err
		}
		if err := a.nos(n.Block, ehCorpoMapLike(n.Directive)); err != nil {
			return err
		}
		fim, err := a.consumir(TokenBlockEnd)
		if err != nil {
			return err
		}
		n.temBloco = true
		n.Span = Span{nome.Start, fim.End}
		return nil

	default:
		return ParseErrors{{
			File:    a.file,
			Line:    proximo.Line,
			Message: fmt.Sprintf("esperava ';' ou '{' apos %q, encontrei %q", n.Directive, proximo.Raw),
			Classe:  RecusaTerminadorAusente,
			Token:   proximo.Raw,
		}}
	}
}

// comentariosDentro filtra os comentarios drenados, ficando so com os que
// caem dentro da cabeca. O ultimo drenar de uma diretiva pode pegar
// comentarios que estao DEPOIS do ultimo argumento ("a b # c\n;"): esses
// ficam fora de HeadSpan, e registra-los seria mentir sobre o intervalo.
func comentariosDentro(toks []Token, head Span) []Span {
	var dentro []Span
	for _, t := range toks {
		if t.Start >= head.Start && t.End <= head.End {
			dentro = append(dentro, Span{t.Start, t.End})
		}
	}
	return dentro
}

// drenarComentarios consome, da posicao atual, quantos TokenComment
// estiverem em sequencia, guardando-os em pendentes na ordem em que
// apareceram. Esses tokens nao entram em Args (crossplane/parse.go:286-290)
// e reaparecem como nos "#" irmaos depois que a diretiva atual (e seu
// bloco, se houver) termina -- e ali que no() os consome de volta da fila,
// em vez de tentar ler um TokenComment novo da posicao atual do stream, que
// nesse ponto ja avancou para alem deles.
//
// pendentes == nil e o corpo map-like: o crossplane descarta esses
// comentarios (parse.go:319 faz continue antes de parse.go:436), entao aqui
// eles tambem sao descartados. Descartados da fila, nao do arquivo: eles
// continuam registrados em coletados, que vira Node.HeadComments.
func (a *aligner) drenarComentarios(pendentes *[]Token, coletados *[]Token) {
	for a.pos < len(a.toks) && a.toks[a.pos].Kind == TokenComment {
		tok := a.toks[a.pos]
		if pendentes != nil {
			*pendentes = append(*pendentes, tok)
		}
		*coletados = append(*coletados, tok)
		a.pos++
	}
}

func (a *aligner) espiar() (Token, error) {
	if a.pos >= len(a.toks) {
		return Token{}, ParseErrors{{
			File:    a.file,
			Message: "fim inesperado da configuracao",
			Classe:  RecusaFimInesperado,
		}}
	}
	return a.toks[a.pos], nil
}

func (a *aligner) consumir(kind TokenKind) (Token, error) {
	tok, err := a.espiar()
	if err != nil {
		return Token{}, err
	}
	if tok.Kind != kind {
		return Token{}, a.tokenInesperado(tok, RecusaTokenInesperado)
	}
	a.pos++
	return tok, nil
}

// consumirNomeDeDiretiva le o token que abre um statement. E separado de
// consumir so pela classe da recusa: o crossplane aceita QUALQUER valor de
// token como nome de diretiva (parse.go:256-261 monta o Directive com
// t.Value sem checar que e uma palavra; so "}" em parse.go:237 e comentario
// em parse.go:264 sao tratados a parte), entao "{}" vira para ele uma
// diretiva chamada "{". Nos recusamos, como o nginx, e a classe existe para
// que o fuzz reconheca essa divergencia pela forma exata do token.
func (a *aligner) consumirNomeDeDiretiva() (Token, error) {
	tok, err := a.espiar()
	if err != nil {
		return Token{}, err
	}
	if tok.Kind != TokenWord {
		return Token{}, a.tokenInesperado(tok, RecusaTokenNoLugarDeDiretiva)
	}
	a.pos++
	return tok, nil
}

func (a *aligner) tokenInesperado(tok Token, classe ClasseRecusa) error {
	return ParseErrors{{
		File:    a.file,
		Line:    tok.Line,
		Message: fmt.Sprintf("coluna %d: token inesperado %q", tok.Column, tok.Raw),
		Classe:  classe,
		Token:   tok.Raw,
	}}
}
