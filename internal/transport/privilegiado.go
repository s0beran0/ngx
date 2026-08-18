package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/s0beran0/ngx/internal/output"
)

// CodigoLeituraPrivilegiada informa que um arquivo so pode ser lido com
// privilegio. Severidade info: nao e problema, e o registro de que houve
// escalada -- ler config de servidor com sudo nao pode acontecer calado.
const CodigoLeituraPrivilegiada = "NGX-0230"

// CodigoPrivilegioNegado cobre o caso em que nem com privilegio deu.
const CodigoPrivilegioNegado = "NGX-0231"

// CodigoLeituraPeloDump informa que o conteudo veio de `nginx -T` porque nem
// a leitura direta nem o `sudo cat` alcancaram o arquivo.
const CodigoLeituraPeloDump = "NGX-0232"

// privilegiado envolve um Transport e, quando a leitura comum topa em
// permissao, repete a leitura daquele arquivo com privilegio.
//
// Por que um decorador e nao um ramo dentro do transporte SSH: a regra vale
// igual para qualquer alvo, e mante-la fora do cliente SSH deixa o transporte
// com uma responsabilidade so. Tambem torna o comportamento testavel sem
// rede, com um Transport falso.
//
// A escalada e MINIMA de proposito. Nao le tudo com sudo: tenta sem
// privilegio primeiro e so repete o arquivo que foi recusado. Numa
// configuracao de 132 arquivos onde um e restrito, 131 continuam sendo lidos
// como o usuario comum, e o diagnostico nomeia o unico que exigiu mais.
type privilegiado struct {
	Transport
	ctx context.Context

	// dump e o ultimo recurso: `nginx -T` executado com privilegio. Existe
	// porque servidor endurecido costuma liberar no sudoers comandos
	// ESPECIFICOS -- tipicamente o proprio nginx -- e nao um `cat` generico.
	// Nesses hosts o `sudo cat` falha e o dump funciona, e sem ele a leitura
	// privilegiada seria inutil justamente onde o sudo e bem configurado.
	//
	// Nao e o primeiro recurso porque `nginx -T` exige configuracao VALIDA:
	// a hora em que mais se precisa ler a configuracao e quando ela quebrou,
	// e ai o dump nao responde. Ler arquivo a arquivo responde sempre.
	dump      func(context.Context) (map[string][]byte, error)
	dumpFeito bool
	dumpCache map[string][]byte
	dumpErro  error

	mu        sync.Mutex
	elevados  []string
	viaDump   []string
	recusados map[string]string
}

// ComLeituraPrivilegiada devolve um Transport que repete com privilegio a
// leitura recusada por permissao. Passar ativo=false devolve o transporte
// original intocado: a decisao de escalar e de quem chama, e a DR5 exige que
// ela seja explicita.
func ComLeituraPrivilegiada(ctx context.Context, tr Transport, ativo bool) Transport {
	return ComLeituraPrivilegiadaEDump(ctx, tr, ativo, nil)
}

// ComLeituraPrivilegiadaEDump acrescenta o ultimo recurso: uma funcao que
// devolve a configuracao efetiva inteira (na pratica, `nginx -T` com
// privilegio), consultada so quando a leitura por arquivo nao alcancou.
func ComLeituraPrivilegiadaEDump(
	ctx context.Context,
	tr Transport,
	ativo bool,
	dump func(context.Context) (map[string][]byte, error),
) Transport {
	if !ativo {
		return tr
	}
	return &privilegiado{Transport: tr, ctx: ctx, dump: dump, recusados: map[string]string{}}
}

// conteudoDoDump devolve o conteudo de um caminho segundo o dump, executando-o
// no maximo uma vez por transporte. Um `nginx -T` por arquivo recusado seria
// absurdo numa configuracao de 132 arquivos.
func (p *privilegiado) conteudoDoDump(caminho string) ([]byte, bool) {
	if p.dump == nil {
		return nil, false
	}
	p.mu.Lock()
	if !p.dumpFeito {
		p.dumpFeito = true
		p.dumpCache, p.dumpErro = p.dump(p.ctx)
	}
	cache, err := p.dumpCache, p.dumpErro
	p.mu.Unlock()

	if err != nil {
		return nil, false
	}
	conteudo, ok := cache[caminho]
	return conteudo, ok
}

func (p *privilegiado) Open(caminho string) (io.ReadCloser, error) {
	rc, err := p.Transport.Open(caminho)
	if err == nil || !ehPermissao(err) {
		return rc, err
	}

	// Argv explicito, sem shell: nome de arquivo com espaco, aspa ou cifrao
	// nao vira injecao. O `--` fecha a lista de opcoes, e e ele que impede a
	// outra injecao, a de ARGUMENTO: sem ele, um caminho comecando com `-`
	// seria lido como flag pelo cat em vez de como arquivo. O caminho vem de
	// diretiva `include` da configuracao do alvo, que nao e entrada confiavel
	// -- e aqui o comando roda com privilegio, entao o custo de errar e alto
	// e o de prevenir e um token.
	//
	// Nao recusamos caminho iniciado por `-`: com o `--` ele funciona, e
	// recusar quebraria um arquivo de nome legitimo por precaucao redundante.
	//
	// O -n do sudo evita ficar pendurado esperando senha num processo sem
	// terminal.
	stdout, stderr, saida, errRun := p.Transport.Run(p.ctx, []string{"sudo", "-n", "cat", "--", caminho})
	if errRun != nil {
		return nil, errRun
	}
	if saida != 0 {
		if conteudo, ok := p.conteudoDoDump(caminho); ok {
			p.registrarViaDump(caminho)
			return io.NopCloser(bytes.NewReader(conteudo)), nil
		}
		p.registrarRecusa(caminho, primeiraLinha(string(stderr)))
		// Devolve o erro ORIGINAL de permissao: para quem chamou, o arquivo
		// segue ilegivel, e a causa continua sendo permissao. O detalhe do
		// que o sudo respondeu vai no diagnostico, nao no erro.
		return nil, err
	}

	p.registrarElevado(caminho)
	return io.NopCloser(bytes.NewReader(stdout)), nil
}

func (p *privilegiado) Glob(padrao string) ([]string, error) {
	achados, err := p.Transport.Glob(padrao)
	if err == nil || !ehPermissao(err) {
		return achados, err
	}

	// Diretorio nao listavel pelo usuario comum. `ls -1` num diretorio so,
	// sem recursao, e o minimo que responde a pergunta.
	dir, arquivo := path.Split(padrao)
	dir = path.Clean(dir)
	// `--` pelo mesmo motivo do cat: sem ele um diretorio cujo nome comece
	// com `-` viraria opcao do ls.
	stdout, stderr, saida, errRun := p.Transport.Run(p.ctx, []string{"sudo", "-n", "ls", "-1", "--", dir})
	if errRun != nil {
		return nil, errRun
	}
	if saida != 0 {
		// O dump ja conhece TODOS os arquivos da configuracao efetiva, entao
		// ele responde ao padrao sem precisar listar diretorio nenhum. E o
		// caso do servidor endurecido: o sudoers libera o nginx e recusa
		// tanto `cat` quanto `ls`.
		if casados, ok := p.globPeloDump(padrao); ok {
			p.registrarViaDump(dir)
			return casados, nil
		}
		p.registrarRecusa(dir, primeiraLinha(string(stderr)))
		return nil, err
	}

	casados := []string{}
	for _, nome := range strings.Split(string(stdout), "\n") {
		nome = strings.TrimSpace(nome)
		if nome == "" {
			continue
		}
		// path.Match, nunca filepath.Match: o alvo remoto usa separador
		// POSIX mesmo quando o ngx roda no Windows.
		if ok, _ := path.Match(arquivo, nome); ok {
			casados = append(casados, path.Join(dir, nome))
		}
	}
	sort.Strings(casados)
	p.registrarElevado(dir)
	return casados, nil
}

// globPeloDump casa o padrao contra os caminhos que o dump conhece. Devolve
// ok=false quando nao ha dump: uma lista vazia com ok=true seria indistinguivel
// de "o padrao nao casou nada", e apresentar configuracao incompleta como
// completa e o defeito que a DR6 existe para impedir.
func (p *privilegiado) globPeloDump(padrao string) ([]string, bool) {
	if p.dump == nil {
		return nil, false
	}
	p.mu.Lock()
	if !p.dumpFeito {
		p.dumpFeito = true
		p.dumpCache, p.dumpErro = p.dump(p.ctx)
	}
	cache, err := p.dumpCache, p.dumpErro
	p.mu.Unlock()

	if err != nil {
		return nil, false
	}

	casados := []string{}
	for caminho := range cache {
		if ok, _ := path.Match(padrao, caminho); ok {
			casados = append(casados, caminho)
		}
	}
	sort.Strings(casados)
	return casados, true
}

func (p *privilegiado) registrarElevado(caminho string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.elevados = append(p.elevados, caminho)
}

func (p *privilegiado) registrarViaDump(caminho string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.viaDump = append(p.viaDump, caminho)
}

func (p *privilegiado) registrarRecusa(caminho, motivo string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recusados[caminho] = motivo
}

// Diagnosticos relata o que exigiu privilegio e o que nem com ele funcionou.
// Escalar em silencio seria esconder do operador que a leitura da
// configuracao do servidor dele passou por sudo.
func (p *privilegiado) Diagnosticos() []output.Diagnostic {
	p.mu.Lock()
	defer p.mu.Unlock()

	diags := []output.Diagnostic{}
	if len(p.elevados) > 0 {
		lista := append([]string(nil), p.elevados...)
		sort.Strings(lista)
		diags = append(diags, output.Diagnostic{
			Severity: output.SeverityInfo,
			Code:     CodigoLeituraPrivilegiada,
			Message: fmt.Sprintf(
				"%d caminho(s) so puderam ser lidos com privilegio, porque --sudo foi "+
					"pedido: %s", len(lista), resumirCaminhos(lista)),
		})
	}
	if len(p.viaDump) > 0 {
		lista := append([]string(nil), p.viaDump...)
		sort.Strings(lista)
		diags = append(diags, output.Diagnostic{
			Severity: output.SeverityInfo,
			Code:     CodigoLeituraPeloDump,
			Message: fmt.Sprintf(
				"%d caminho(s) vieram de `nginx -T` com privilegio, porque o sudo do "+
					"alvo nao permite ler arquivo diretamente: %s",
				len(lista), resumirCaminhos(lista)),
		})
	}

	for caminho, motivo := range p.recusados {
		diags = append(diags, output.Diagnostic{
			Severity: output.SeverityError,
			Code:     CodigoPrivilegioNegado,
			File:     caminho,
			Message: fmt.Sprintf(
				"nem com privilegio foi possivel ler este caminho (%s); confira se o "+
					"sudo do alvo permite `cat` sem senha para o usuario da conexao", motivo),
		})
	}
	return diags
}

// ehPermissao reconhece a recusa por permissao vinda de qualquer alvo. O erro
// do SFTP casa com fs.ErrPermission -- verificado contra um servidor real --,
// entao a mesma checagem serve ao local e ao remoto.
func ehPermissao(err error) bool {
	return errors.Is(err, fs.ErrPermission)
}

func primeiraLinha(texto string) string {
	texto = strings.TrimSpace(texto)
	if i := strings.IndexByte(texto, '\n'); i >= 0 {
		return texto[:i]
	}
	if texto == "" {
		return "sem detalhe do sudo"
	}
	return texto
}

// resumirCaminhos evita despejar uma lista de centenas de caminhos numa linha
// de diagnostico. A contagem exata ja aparece antes; aqui bastam exemplos
// para o operador reconhecer do que se trata.
func resumirCaminhos(lista []string) string {
	const mostrar = 3
	if len(lista) <= mostrar {
		return strings.Join(lista, ", ")
	}
	return fmt.Sprintf("%s e mais %d",
		strings.Join(lista[:mostrar], ", "), len(lista)-mostrar)
}

// Diagnosticos recolhe o que um transporte observou, quando ele tem algo a
// contar. Existe como funcao e nao como metodo da interface porque so o
// transporte privilegiado tem algo a relatar: acrescentar isso a Transport
// obrigaria toda implementacao a carregar um metodo vazio.
func Diagnosticos(tr Transport) []output.Diagnostic {
	if d, ok := tr.(interface{ Diagnosticos() []output.Diagnostic }); ok {
		return d.Diagnosticos()
	}
	return nil
}
