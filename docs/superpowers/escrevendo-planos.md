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
