# Avaliação do FazTudo

Mede, com números, se uma mudança de configuração (provedor, modelos, esforço, best-of-N) melhora a
qualidade das entregas. Inspirado no BuffBench do Codebuff.

```bash
go run . -eval evals/example.json
```

Para cada **tarefa** × **braço**, o FazTudo copia o repositório-modelo para
`<output>/<data>/<tarefa>__<braço>` (sem `.git`; `node_modules` e `.venv` são linkados), roda o
pipeline completo em execução real e pontua o resultado de 0 a 100:

| Critério | Peso | Como |
|---|---:|---|
| Checagens ocultas | 40 | `hidden_checks` rodam só depois do pipeline; os agentes nunca as veem |
| Rastreabilidade | 25 | IDs de teste da especificação encontrados nos arquivos de teste |
| Entrega validada | 20 | execução concluída **e** validação `passed` (todos os portões aprovaram com evidência) |
| Legibilidade | 15 | −1,5 por achado de sintaxe/formatação/linha longa na última verificação executada |

Critério sem observação (sem `hidden_checks`, sem IDs de teste, sem verificação executada, validação
não avaliada) vale **0** e aparece na coluna *Não avaliado*: ausência de evidência nunca dá nota máxima.
Resultados simulados (`dry_run`) aparecem no relatório, mas ficam **fora do ranking**. Só entra no
ranking resultado real, com verificação executada e checagens ocultas rodadas; os demais aparecem com
o motivo.

O relatório (`report.md`) traz:

- **Segurança da aprovação:** toda tarefa `"expect": "blocked"` (uma US que não pode ser entregue como
  pedida) aprovada ou promovida vira violação e zera a pontuação. Isso é conferido na promoção e na
  validação, não só pela nota.
- **Por braço:** número de execuções, média, mínimo, máximo e desvio padrão (com menos de 3 execuções
  aparece *amostra insuficiente*), entregas aceitas, **falhas aceitas indevidamente** (aprovadas pelo
  pipeline e reprovadas pelas checagens ocultas), duração média, tokens, custo informado, custo
  estimado e chamadas de custo desconhecido.
- **Por tarefa:** as correções necessárias, o número de chamadas de agentes, os tokens e a duração.
- **Limitações** da comparação. Cada pasta de trabalho guarda o código entregue, o histórico git
(um commit por etapa) e `.faztudo-eval.log`.

## Formato da suíte

```json
{
  "output": "eval-results",
  "dry_run": false,
  "tasks": [
    {
      "name": "supermercado",
      "template": "templates/supermercado",
      "story": "EDD_supermercado_gemeo_digital.md",
      "checks": [{ "name": "unit", "run": "npm run test:unit" }],
      "hidden_checks": [
        { "name": "unit", "run": "npm run test:unit" },
        { "name": "e2e", "run": "npx playwright test" }
      ],
      "timeout_min": 90
    }
  ],
  "arms": [
    { "name": "claude", "provider": "claude" },
    { "name": "codex-luna", "provider": "codex", "std_model": "gpt-5.6-luna" },
    { "name": "claude-bo3", "provider": "claude", "candidates": 3 }
  ]
}
```

Campos opcionais:

| Campo | Onde | O que faz |
|---|---|---|
| `repeat` | suíte | repete cada braço em cada tarefa (até 20) para medir a dispersão |
| `share_dependencies` | suíte | liga `node_modules`/`.venv` do template em vez de copiar; mais rápido, mas um agente de um braço pode alterar o que os outros usam |
| `hidden_dir` | tarefa | pasta **fora** do template com os testes reservados, copiada para a cópia só depois do pipeline: nenhum agente a lê |
| `expect` | tarefa | `"blocked"`: caso de segurança da aprovação |
| `kind` | tarefa | rótulo do piloto (bug, funcionalidade, autorização, migration, interface, desempenho) |
| `flow` | braço | fluxo de agentes (`full`, `balanced`, `fast`, `audit`) |
| `policy` | braço | campos da política sobrepostos ao padrão, como em `config.json` |
| `prompts` | braço | textos de etapa sobrepostos, como a tela *Prompts* salva |
| `mode` | braço | `"direct"`: um agente recebe a US e os comandos e trabalha sozinho, como o CLI usado direto; é o braço de referência do piloto |

Caminhos relativos são resolvidos a partir do arquivo da suíte. Campos de modelo omitidos usam a
configuração salva. O failover fica desligado para que cada braço meça somente o provedor que nomeia.
Use `"dry_run": true` para validar a suíte sem consumir tokens.

Boas tarefas têm uma especificação com IDs de teste e `hidden_checks` independentes da implementação
(testes de aceitação escritos por você, não pelos agentes).
