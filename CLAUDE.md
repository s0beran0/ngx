# ngx

CLI em Go que torna o nginx operável por agentes de IA: saída JSON estruturada,
leitura por seletor, mudanças transacionais com rollback. Projeto pessoal de
Eduardo Benck, open source sob MIT.

Design e decisões: `docs/superpowers/specs/`. Planos de implementação:
`docs/superpowers/plans/`.

## Convenções

- Go 1.25, zero CGO, binário estático.
- Comentários de código em português, sem acentuação.
- **Mensagens de commit nunca mencionam Claude, IA ou co-autoria.** Sem trailer
  `Co-Authored-By`, sem "Generated with". Autoria exclusiva do Eduardo.
- Nenhuma menção a SEA Tecnologia em código, licença ou documentação.
- Toda lista em JSON serializa como `[]`, nunca `null` — um agente que faz
  `.length` numa lista nula quebra.
- Campo indisponível é omitido, nunca estimado. Ausência é informação; número
  errado é mentira.
- Nenhum `exec` de shell. `exec.Command` com argv explícito.

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
