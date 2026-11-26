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
Resultados simulados (`dry_run`) aparecem no relatório, mas ficam **fora do ranking**.

O relatório (`report.md`) traz a média por braço e, por tarefa, as correções necessárias, o número de
chamadas de agentes e a duração. Cada pasta de trabalho guarda o código entregue, o histórico git
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

Caminhos relativos são resolvidos a partir do arquivo da suíte. Campos de modelo omitidos usam a
configuração salva. O failover fica desligado para que cada braço meça somente o provedor que nomeia.
Use `"dry_run": true` para validar a suíte sem consumir tokens.

Boas tarefas têm uma especificação com IDs de teste e `hidden_checks` independentes da implementação
(testes de aceitação escritos por você, não pelos agentes).
