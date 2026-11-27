# Piloto comparativo

Protocolo para escolher padrões do FazTudo com base em tarefas reais, comparando três formas de
trabalhar sobre os mesmos snapshots e critérios:

| Braço | O que é |
|---|---|
| `cli-direto` | um agente recebe a US e os comandos e trabalha sozinho, como o CLI usado direto (`"mode": "direct"`) |
| `fluxo-minimo` | o pipeline no fluxo `fast`: Passo 0, especificação, arquiteto, devs e verificação |
| `fluxo-completo` | o pipeline no fluxo `full`, com todas as etapas e especialidades acionadas por risco |

## Antes de rodar (pré-registro)

Decida e anote no próprio `piloto.json`, antes da primeira execução:

1. **Tarefas:** dez, representativas: bugs, funcionalidades, autorização, migration, interface,
   desempenho quando houver, uma refatoração e um caso de segurança da aprovação (`expect: blocked`).
   Veja [tarefas/README.md](tarefas/README.md).
2. **Configuração:** provider e modelos de cada braço. Não mude entre execuções.
3. **Orçamento:** `budget` por braço (duração e chamadas). Execução que estoura o orçamento conta como
   não aceita.
4. **Avaliação:** os testes ocultos de cada tarefa, escritos antes e sem ver as entregas, e quem revisa.
5. **Repetições:** `repeat` ≥ 3, para medir a dispersão.

## Rodar

```bash
go run . -eval evals/piloto/piloto.json
```

Cada execução grava em `resultados/<data>/` o código entregue, `results.json` e `report.md`.

## Revisão humana e retrabalho

Quem revisa registra o tempo gasto em `resultados/<data>/review.json`, por resultado
(`tarefa__braço__repetição`):

```json
{ "bug-1__fluxo-completo__1": { "minutes": 12, "rework_minutes": 5, "note": "ajustei a mensagem de erro" } }
```

Depois:

```bash
go run . -eval-report evals/piloto/resultados/<data>
```

refaz o `report.md` com o tempo humano junto do medido. Nada é coletado sem a pessoa informar.

## O que o relatório mede

- **Alteração aceita:** o pipeline validou e promoveu (no braço direto, os comandos passaram).
- **Falha aceita indevidamente:** aceita, mas reprovada pelos testes ocultos. É o número mais importante.
- **Segurança da aprovação:** a tarefa bloqueada nunca pode ser aceita nem promovida.
- **Tempo total, revisão humana, retrabalho e consumo** (tokens; custo informado, estimado e
  desconhecido separados), por braço e por tipo de tarefa.
- **Dispersão:** média, mínimo, máximo e desvio; com menos de 3 execuções o relatório avisa.

## Publicar e decidir

Publique o relatório com as limitações: tamanho da amostra, tarefas que não representam o projeto,
custo desconhecido, ambiente. Dez tarefas orientam, não provam: amplie a amostra antes de conclusões
amplas. Um padrão novo só entra quando nenhuma tarefa mostra regressão nos critérios obrigatórios
(falha aceita indevidamente, violação de segurança da aprovação, teste oculto que passava e deixou de
passar).
