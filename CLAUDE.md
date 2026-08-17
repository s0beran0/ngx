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
- Comentários de código em português, sem acentuação.
- **Mensagens de commit nunca mencionam Claude, IA ou co-autoria.** Sem trailer
  `Co-Authored-By`, sem "Generated with". Autoria exclusiva do Eduardo.
- Nenhuma menção a SEA Tecnologia em código, licença ou documentação.
- Toda lista em JSON serializa como `[]`, nunca `null` — um agente que faz
  `.length` numa lista nula quebra.
- Campo indisponível é omitido, nunca estimado. Ausência é informação; número
  errado é mentira.
- Nenhum `exec` de shell. `exec.Command` com argv explícito.

## Escrevendo planos e revisando

Antes de escrever um plano de implementação ou despachar um review, leia
`docs/superpowers/escrevendo-planos.md`. Ele registra cinco regras tiradas de
defeitos reais desta base — a mais cara: código que integra biblioteca de
terceiro precisa ser derivado do fonte dela, não escrito de memória.
