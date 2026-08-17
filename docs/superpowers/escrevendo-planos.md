# Escrevendo planos e revisando neste projeto

Leia isto **antes de escrever um plano de implementação** ou de despachar um
review. Não é invariante de código — por isso não está no `CLAUDE.md`.

## Escrevendo planos de implementação

Estas regras vêm de defeitos reais desta base de código, não de teoria. Cada uma
tem o custo que ela evita anotado.

### Código que depende de biblioteca de terceiro não se escreve de cabeça

Se uma tarefa integra uma biblioteca externa, o código do plano **precisa ser
derivado do fonte dela**, não da lembrança de como ela funciona. Leia o fonte no
module cache antes de escrever o passo, e cite no brief o arquivo e a função que
definem o comportamento.

Um plano com código completo mas errado é pior que um plano com descrição: o
implementador transcreve fielmente, os testes que você escreveu junto concordam
com o código que você escreveu, e o defeito só aparece no review — ou em
produção.

*Custo evitado:* `Parse` ignorava `payload.Errors` do crossplane, então um
`.conf` com erro de sintaxe produzia árvore vazia com exit 0. O tokenizador
quebrava em `{` sem tratar `${var}`, fazendo o `ngx` rejeitar qualquer
configuração com template de variável. Os dois passaram por implementação e só
caíram no review.

### Quando não der para saber, mande investigar em vez de adivinhar

Se você não consegue determinar o comportamento correto ao escrever o plano,
**não escreva código provisório**. Escreva um passo que manda o implementador ler
a fonte específica e derivar o comportamento, com o critério de aceitação
explícito. Um brief que diz "leia `lex.go` e faça o tokenizador concordar com ele
token a token" produz código melhor que um brief com um tokenizador plausível.

### Testes precisam de um oráculo externo

Um teste cuja expectativa você derivou da mesma cabeça que escreveu a
implementação só confirma que você foi consistente. Quando existir uma referência
independente — a biblioteca que estamos espelhando, o binário real do nginx, um
corpus de arquivos conhecidos — teste contra ela.

Cuidado particular com property tests e fuzz: verifique que a propriedade **pode
falhar**. Antes de aceitar um fuzz como rede de segurança, quebre a implementação
de propósito e confirme que ele acusa.

*Custo evitado:* o fuzz do tokenizador rodou 9,5 milhões de execuções e provava
apenas ausência de panic. Suas quatro asserções eram tautologias — a principal,
`src[Start:End] == Raw`, é impossível de falhar porque o código constrói `Raw`
fatiando `src[start:pos]` e grava `Start`/`End` na mesma expressão. O bug do
`${var}` sobreviveu por essa brecha. A substituição — comparar token a token
contra `crossplane.Lex` — o teria achado em segundos.

### Fix rounds que exigem lógica nova são implementação, não conserto

O momento de maior risco não é a implementação inicial: é a correção. Um
implementador consertando um achado sob pressão inventa mecanismo novo sem o
cuidado que teve na primeira passada, e ninguém revisou aquele mecanismo.

Quando um fix introduzir estrutura que não existia — um cache, um tipo de erro,
uma goroutine, um lock — trate o re-review como review completo, no modelo mais
capaz, e peça julgamento explícito sobre a lógica nova. Não use o re-review
escopado barato, que só confere itens de uma lista.

*Custo evitado:* o fix que corrigiu cinco achados no `Parse` introduziu um data
race e um truncamento silencioso de arquivo. O truncamento era pior que o
problema original: os spans ficavam coerentes com um `Source` truncado, e na v0.2
uma escrita por substituição de bytes truncaria o arquivo real do usuário.

### Nunca despache um loop sem condição de parada

Instruções como "rode o fuzz e corrija os casos que ele encontrar" ou "itere
até passar" não têm fim quando a busca é aberta. Cada correção revela o
próximo caso, e o agente fica horas moendo rendimento decrescente sem
perceber, porque a cada passo ele está de fato achando algo real.

Dê sempre um teto explícito: um número de rodadas, um limite de tempo, ou um
critério de suficiência ("pare quando os N itens listados estiverem
endereçados, mesmo que o fuzz ainda ache casos"). E peça que o que sobrar
vire **entregável** — uma lista de divergências conhecidas com entrada,
comportamento esperado e observado — em vez de trabalho abandonado.

*Custo evitado:* um fix de tokenizador rodou 37 minutos e 287 mil tokens
porque o dispatch mandava corrigir tudo que o fuzz diferencial encontrasse. O
achado que justificava a rodada — um bug que fazia o CLI recusar configuração
válida — apareceu nos primeiros minutos; o resto foi cauda.

### Trave contratos com valores literais

Quando um valor é contrato — exit code, tag JSON, nome de campo consumido por
outro módulo — o teste precisa assertar o **literal**, não a constante simbólica.
`require.Equal(t, ExitDrift, CodeOf(err))` passa depois de alguém trocar
`ExitDrift` de 7 para 8; `require.Equal(t, 7, int(ExitDrift))` não.

## Revisando

- Diga ao revisor o que está em jogo naquela tarefa específica, não só "revise".
  A qualidade do achado acompanha a qualidade do enquadramento.
- Peça verificação negativa: que o revisor quebre o código e confirme que o teste
  acusa. Um revisor que reverteu o `leituraEspelhada` e mediu 8 falhas em 8
  execuções deu mais informação que o implementador, que reportou 2 em 3.
- Nunca instrua um revisor a não sinalizar algo. Se você acha que um achado seria
  falso positivo, deixe-o aparecer e decida depois, registrando a decisão.

## Despachando subagentes sem queimar contexto

Cada subagente é uma sessão própria: relê o próprio histórico a cada turno e
não aparece no `/context` de quem coordena. Numa execução real deste projeto,
os subagentes consumiram cerca de 1,7 milhão de tokens contra 1,1 milhão dos
revisores — e o agente mais caro foi um Sonnet, não um Opus. **Trocar de modelo
não é a alavanca; reduzir turnos e saída verbosa é.**

### O que escrever em todo dispatch

- **`go test ./...` sem `-v` por padrão.** Use `-v` apenas quando algo falhar e
  você precisar do detalhe. A saída verbosa da suíte deste projeto passa de
  16 KB; a compacta cabe em 300 bytes.
- **Nunca cole saída longa no relatório.** Reporte a conclusão e o caminho do
  arquivo. Quem quiser o detalhe abre o arquivo.
- **Relatório final curto e fixo:** STATUS, commits, uma linha de teste,
  concerns. O detalhe vai no arquivo de relatório, não na resposta.
- **Dê teto a qualquer busca aberta** — número de rodadas ou limite de tempo —
  e peça que o que sobrar vire lista de divergências conhecidas. Ver a regra
  sobre loops sem condição de parada.

### RTK está ativo nesta máquina

O `rtk` intercepta comandos de shell e comprime a saída antes de ela entrar no
contexto. Está instalado via Homebrew (`homebrew-core`, Apache 2.0) e ligado
por um hook `PreToolUse` global.

O que isso muda na prática:

- `go test` é reescrito automaticamente. A suíte inteira reporta
  `Go test: 160 passed in 5 packages` em vez de 16 KB de saída. **Falhas não
  são escondidas** — verificado quebrando um teste de propósito: o RTK mostra
  nome, arquivo, linha e mensagem, e informa o caminho da saída completa, que
  ele salva em disco.
- **`git` está excluído da interceptação**, de propósito. O fluxo de review
  gera pacotes com `git diff a..b > arquivo`, e o proxy colapsaria o diff para
  `--stat` — deixando o revisor com nomes de arquivo e nenhum conteúdo. Falha
  silenciosa que nenhum teste acusaria. A exclusão está em
  `~/Library/Application Support/rtk/config.toml`.
- **`Read`, `Grep` e `Glob` do Claude Code não passam pelo hook.** Como boa
  parte do consumo dos subagentes é leitura de arquivo, o ganho real aqui é
  menor que os 60–90% anunciados. Ele ajuda em `go test`, não em tudo.

### O custo de um subagente é quadrático no número de turnos

Medido nos transcripts reais desta execução, não estimado:

| | |
|---|---|
| contexto inicial de todo agente | ~14.900 tokens |
| contexto final do agente mais longo (232 turnos) | ~325.000 tokens |
| custo médio de um turno adicional | ~114.000 tokens de histórico relido |
| histórico relido, total | ~131 milhões de tokens |
| conteúdo gerado pelo modelo, total | ~539 mil tokens |

Para cada token que o modelo escreveu, 243 foram de histórico reenviado. Cada
turno relê todo o contexto acumulado, e o contexto cresce a cada turno — então
o custo não cresce com o número de turnos, cresce com o **quadrado** dele. Um
agente de 232 turnos custou 44 milhões; um de 104 custou 11 milhões.

**Consequência prática, que é contraintuitiva:** prefira **mais agentes
curtos** a um agente longo. Dividir 232 turnos em dois agentes de 116 custa
cerca de 45% menos, mesmo pagando a base de 15 mil tokens duas vezes.

**O que NÃO adianta:** comprimir a saída das ferramentas. Todo o conteúdo de
resultado de ferramenta somado — Read, Bash, Edit, Write — dá ~245 mil tokens,
ou 0,08% do histórico relido. Ferramentas de compressão de saída atacam essa
fatia. Elas não são inúteis, mas não são a alavanca.

**O que adianta, em ordem:**

1. **Tetos de turno.** Toda busca aberta precisa de limite. O agente mais caro
   desta execução foi o do loop de fuzz sem condição de parada.
2. **Dividir tarefa longa em dois dispatches** em vez de um, com o segundo
   recebendo o estado por arquivo em vez de por contexto herdado.
3. **Dizer exatamente qual arquivo ler.** Cada turno de exploração custa o
   contexto inteiro de novo.
4. **Base menor** — menos skills e menos servidores MCP reduzem os ~14.900 de
   todo agente, o que se multiplica pelo número deles.

### As tres regras de dispatch, e a evidencia de cada uma

Um estudo empirico em SWE-bench com Claude Sonnet, GPT-4.1 e Gemini 2.5 Pro
mediu estrategias de controle de turno (arXiv 2510.16786). O resultado que
importa: **limitar turnos nao piorou a qualidade — melhorou.** Orcamento
apertado forca o agente a ir direto ao ponto em vez de explorar.

| Estrategia | Taxa de sucesso | Custo |
|---|---|---|
| Limite fixo no percentil 75 (Claude) | −5,3% | −23,6% |
| Limite **dinamico** — comeca baixo, estende uma vez (Claude) | **+1,6%** | −15,6% adicionais |

A Anthropic recomenda o mesmo caminho por outro angulo: compaction,
note-taking externo, isolamento em sub-agentes e carregamento just-in-time —
tudo arquitetura e disciplina, fechando com "faca a coisa mais simples que
funciona". Nenhuma ferramenta e necessaria.

#### 1. Orcamento de turno: o teto e consequencia do escopo, nao um numero

**Nada aplica o teto.** Medido aqui: a ferramenta que lanca subagente nao tem
parametro de orcamento, e hook `PreToolUse` **nao dispara dentro de
subagente** — duas sondas, registradas no `settings.local.json` do projeto e
tambem no `~/.claude/settings.json` global, 6 chamadas de ferramenta de
subagente, zero entradas no log; so a sessao de quem coordena aparece.
Portanto o teto e texto no prompt: um pedido, nunca uma barreira.

Um pedido so e atendido se for atendivel. O mesmo agente, mesmo modelo, no
mesmo dia:

| dispatch | itens de trabalho | teto | usou |
|---|---|---|---|
| fix round 2 da Task 9 | 4 achados + 1 ruling + 2 opcionais | 50 | **70** |
| adendo do `include .;` | 1 achado | 25 | **13** |

O agente nao "desobedeceu" no primeiro: ele escolheu terminar o trabalho em
vez de largar pela metade, o que era a decisao certa diante do que foi
pedido. **Quando o teto e a tarefa se contradizem, ganha a tarefa.**

Entao a regra e sobre escopo, nao sobre o numero:

- **Um defeito por dispatch.** Se voce esta escrevendo o terceiro item numa
  lista, quebre em dois dispatches. Foi isso que fez o teto valer.
- **A condicao de parada tem que ser verificavel, nao contavel.** Modelo nao
  conta com precisao as proprias chamadas; ele sabe dizer se o teste ficou
  verde. Prefira "pare quando a suite passar e commite" a "pare na chamada
  50".
- **Peca o numero de volta no relatorio.** Nao impede o estouro, mas o torna
  visivel — foi assim que o 70 contra 50 apareceu.
- **A unica barreira real e quem coordena.** Se um agente passar do previsto,
  mate e redespache com escopo menor; nao estenda por comodidade.

Calibragem observada neste projeto, ja com escopo unitario: transcricao de
codigo do brief, 20-30; implementacao com investigacao, 40-50; review com
sonda, 30-40. Acima de 60 o custo por turno ja dobrou em relacao ao inicio.

#### 2. Note-taking externo para quebrar o quadratico

Quando um agente atinge o teto, ele grava o estado num arquivo e **encerra**.
A continuacao vai para um agente NOVO, que le o arquivo e comeca com ~15 mil
tokens de contexto em vez de herdar 300 mil.

Isso converte um custo quadratico em varios lineares. Dois agentes de 116
turnos custam cerca de 45% menos que um de 232, mesmo pagando a base duas
vezes. **Nao retome um agente longo por comodidade** — retomar preserva o
contexto inteiro e e justamente o que se quer evitar.

#### 3. Just-in-time dirigido

Diga qual arquivo ler. Cada turno de exploracao repaga o contexto acumulado,
entao "descubra onde esta X" custa muito mais que "leia X em caminho/y.go".
Quando nao souber o caminho, mande localizar com uma chamada dirigida em vez
de deixar o agente vagar.

### O prompt de dispatch e relido a cada turno — corte-o

Medido nos transcripts: o prompt medio de dispatch tinha **896 tokens**, e o
total relido por causa deles foi **1,74 milhao de tokens** — sete vezes tudo
que uma ferramenta de compressao de saida poderia poupar. Um prompt de 1.234
tokens num agente de 104 turnos custou 128 mil sozinho.

A maior parte era redundante: restricoes globais que **ja estao no CLAUDE.md**
que o agente carrega, e requisitos que **ja estao no brief** que ele vai ler.

**Nao repita no dispatch o que o CLAUDE.md ja diz.** Commits sem mencao a IA,
comentarios em portugues sem acentuacao, zero CGO, listas JSON como `[]` —
tudo isso ja chega ao agente. Repetir custa a cada turno dele.

#### Template de dispatch (alvo: 300-400 tokens)

```
<uma frase: o que fazer e onde>

## Escopo
UM defeito/tarefa. Pare quando <condicao verificavel: a suite passar, o teste
X ficar verde> e commite. Referencia de ~N chamadas de ferramenta: se passar
disso sem terminar, pare, grave o estado no relatorio e reporte — nao
continue. Diga no relatorio quantas chamadas usou.

## Arquivos
<caminhos exatos. Diga o que NAO precisa ler.>

## O que fazer
<so o que o brief nao cobre, ou o que mudou desde que ele foi escrito>

## Ao terminar
`go test ./... -race` (sem -v). Commite. Relatorio em <caminho>.
Resposta final: STATUS, commit, uma linha de teste, uma linha por item.
```

O "por que isto importa" motiva o agente e as vezes vale — mas cobra o preco
a cada turno. Use uma frase, nao um paragrafo.

### Tres desperdicios menores, tambem medidos

**`Write` falha em 16% das chamadas.** Agentes em worktree isolado tentam
escrever o relatorio no `.superpowers/` do repo principal, o isolamento
bloqueia, e eles tentam de novo. Cerca de 15 turnos perdidos. **Diga no
dispatch para escrever direto na copia dentro do worktree**, sem tentar o
caminho compartilhado.

**89 comandos Bash repetidos pelo mesmo agente** — `cd` refeito 12 vezes, o
mesmo fuzz rodado 9 vezes. O diretorio de trabalho **persiste** entre chamadas
de Bash; diga isso quando o agente precisar navegar. E agrupe verificacoes num
comando so em vez de uma por turno.

**Bash e a ferramenta mais chamada** (642 contra 238 de Read). Como o custo e
por turno, encadear tres verificacoes num unico comando vale mais que otimizar
o que cada uma imprime.

### O maior gargalo e a sessao de coordenacao, nao os subagentes

Medido nos transcripts desta execucao:

| | turnos | historico relido | por turno |
|---|---|---|---|
| 41 subagentes somados | 1.137 | 131,0 M | 115 mil |
| a sessao de coordenacao | 687 | **309,5 M** | **450 mil** |

**A sessao principal foi 70% do custo total.** O contexto dela comecou em 22
mil tokens e chegou a 844 mil — e cada turno relê tudo.

O que a inflou: **os relatorios dos subagentes voltam inteiros para ela.** Um
review que retorna 3 mil tokens de analise, recebido 300 turnos antes do fim,
custa ~900 mil tokens sozinho, porque e relido em cada turno seguinte.

Isso contraria a orientacao da Anthropic sobre sub-agentes, que e retornarem
**sumarios condensados de 1.000 a 2.000 tokens** depois de explorar
extensivamente.

#### A regra: o retorno do subagente e um sumario, nao o trabalho

Todo dispatch precisa exigir, explicitamente:

> Escreva a analise completa em `<caminho do relatorio>`. Como resposta final,
> devolva no maximo 15 linhas: veredito numa linha, cada achado como uma linha
> (severidade + titulo + arquivo:linha), e o caminho do relatorio. **Nao repita
> a analise na resposta** — quem coordena le o arquivo se precisar.

O relatorio completo continua existindo e continua sendo lido — sob demanda,
uma vez, por quem precisa. A diferenca e que ele deixa de ser reenviado a cada
turno da sessao de coordenacao pelo resto da execucao.

#### E a coordenacao precisa de higiene de contexto

- **`/compact` ao fim de cada fase**, nao quando degradar. Um resumo feito de
  uma sessao saudavel e melhor que um feito de uma sessao ja confusa.
- **`/clear` ao trocar de assunto** — investigar consumo de tokens e executar
  um plano de implementacao nao precisam compartilhar contexto.
- **Nao imprimir arquivo inteiro no terminal** para "conferir". `wc -l`,
  `grep -c` e `tail -5` respondem a mesma pergunta por uma fracao do custo.
