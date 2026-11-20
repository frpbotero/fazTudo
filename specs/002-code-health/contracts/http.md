# Contrato: API local (127.0.0.1)

## GET /api/health/areas

`200` → `AreaSuggestion` ([data-model](../data-model.md)).
`422` repositório não configurado (como os demais endpoints). Sem efeitos colaterais; não chama modelo e só
lê o `graph.json` que já existir (não reconstrói o grafo).

## POST /api/health/start

Corpo: `CleanupOptions`.

| Status | Quando |
|---|---|
| `202` | aceita; acompanhar por `GET /api/status` e `GET /api/events` |
| `400` | área inválida (inexistente, fora do repositório, `..`), `rounds` fora de 1–5 |
| `409` | execução em andamento, repositório bloqueado, política sem git em execução real |

Árvore suja não é recusada aqui: como nas US, a execução começa e falha na preparação com a mensagem do git.

## Mudanças em respostas existentes

- `GET /api/status`: `Run.kind` e `Run.cleanup` ([data-model](../data-model.md)).
- `POST /api/pipeline/stop`: também para a limpeza.
- `POST /api/pipeline/resume` e `/retry` com `kind = cleanup`: `409` com mensagem "Limpeza não é retomável;
  inicie uma nova."

Mensagens de erro em português, no mesmo formato JSON de erro dos endpoints existentes.
