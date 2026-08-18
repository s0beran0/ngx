# ngx

CLI em Go que torna o nginx operável por agentes de IA: saída JSON estruturada,
leitura por seletor, mudanças transacionais com rollback. Projeto pessoal de
Eduardo Benck, open source sob MIT.

Design e decisões: `docs/superpowers/specs/`. Planos de implementação:
`docs/superpowers/plans/`.

## Dois públicos, uma ferramenta

O `ngx` é usado por **agentes de IA** e por **humanos**, e isso não é acidente:
a saída é JSON quando stdout não é um terminal, e legível quando é. `--json` e
`--human` forçam. Todo comando novo precisa servir aos dois.

Onde o comportamento diverge, a divergência é deliberada e vira regra de
segurança: `--no-redact` só é aceito em terminal, porque um humano depurando
pode ver o segredo e um agente lendo o pipe, estruturalmente, não consegue nem
pedir.

**Cuidado com a palavra "agente".** Ela aparece com dois sentidos no projeto:
o *agente de IA* que consome a saída, e o `ssh-agent`, programa do sistema
operacional que guarda chaves SSH e não tem relação nenhuma com IA. Escreva
sempre `ssh-agent` com o prefixo; "agente" sozinho significa o consumidor.
Confundir os dois leva a implementar a coisa errada.

## Convenções

- Go 1.25, zero CGO, binário estático.
- **Tudo que sai do repositório é em inglês**: comentários de código,
  mensagens de diagnóstico, textos de `--help`, README e `docs/`. É projeto
  open source e quem contribui ou consome a saída não fala necessariamente
  português. Sem acentuação em comentário de código.
- Os planos e specs em `docs/superpowers/` **ficam em português**: são registro
  de decisão deste projeto, não documentação de usuário.
- **Mensagens de commit nunca mencionam Claude, IA ou co-autoria.** Sem trailer
  `Co-Authored-By`, sem "Generated with". Autoria exclusiva do Eduardo.
- Nenhuma menção a SEA Tecnologia em código, licença ou documentação.
- Toda lista em JSON serializa como `[]`, nunca `null` — um agente que faz
  `.length` numa lista nula quebra.
- Campo indisponível é omitido, nunca estimado. Ausência é informação; número
  errado é mentira.
- Nenhum `exec` de shell. `exec.Command` com argv explícito.

## Despachando subagentes

**O retorno de um subagente e um sumario, nunca o trabalho.** Todo dispatch
exige, com estas palavras:

> Escreva a analise completa em `<caminho>`. Como resposta final, no maximo
> 15 linhas: veredito numa linha, cada achado como uma linha (severidade +
> titulo + arquivo:linha), e o caminho do relatorio. Nao repita a analise na
> resposta.

*Por que:* o retorno entra no contexto de quem coordena e e relido em **todo
turno seguinte**. Medido nesta base: a sessao de coordenacao consumiu 309
milhoes de tokens de historico relido contra 131 milhoes de 41 subagentes
somados — 70% do custo total — porque os relatorios voltavam inteiros. Um
review de 3 mil tokens recebido cedo custa perto de um milhao sozinho.

**Um defeito por dispatch.** Nada aplica teto de chamadas: a ferramenta de
subagente nao tem esse parametro e hook `PreToolUse` nao dispara dentro de
subagente (medido — sonda no projeto e no global, zero entradas). O teto e um
pedido, e so e atendido quando a tarefa cabe nele: o mesmo agente estourou
70 contra um teto de 50 com 6 itens, e usou 13 contra 25 com um item. Se voce
esta escrevendo o terceiro item da lista, quebre em dois dispatches.

Declare mesmo assim uma referencia de chamadas, uma **condicao de parada
verificavel** ("pare quando a suite passar e commite" — modelo nao conta as
proprias chamadas, mas sabe ler um teste), e peca o numero usado no
relatorio, que e o que torna o estouro visivel. Limitar turnos nao piora a
qualidade: num estudo em SWE-bench, teto dinamico melhorou a taxa de sucesso
do Claude em 1,6% e cortou 15,6% do custo. Aqui rendeu 35% com os mesmos
achados.

Detalhe, template de dispatch e as demais regras:
`docs/superpowers/escrevendo-planos.md`.

## Escrevendo planos e revisando

Antes de escrever um plano de implementação ou despachar um review, leia
`docs/superpowers/escrevendo-planos.md`. Ele registra cinco regras tiradas de
defeitos reais desta base — a mais cara: código que integra biblioteca de
terceiro precisa ser derivado do fonte dela, não escrito de memória.
