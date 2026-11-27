# Estrutura interna

O FazTudo é um monólito modular em Go. Esta página diz onde fica cada decisão, para quem for mudar o
pipeline sem precisar ler tudo.

## `internal/pipeline`

| Arquivo | Responsabilidade |
|---|---|
| `state.go` | tipos do estado da execução (`Run`, `StoryRun`, `Step`), lidos pelo painel, pelo console e pelo journal |
| `orchestrator.go` | ciclo de vida: `Start`, `RetryFailed`, preparação da sessão, desenvolvimento de uma US etapa a etapa, fechamento |
| `outputs.go` | o que Passo 0, especificação e arquiteto decidem para o resto da US |
| `gates.go` | portões: veredito, recuperação de formato, rodadas de correção, validação |
| `verifier.go` | verificação executável: comandos, checagens estáticas, rastreabilidade |
| `calls.go` | chamada de agente: execução, novas tentativas, registro do resultado, fim da execução |
| `scheduler.go` | fila única de agentes e trava de recursos |
| `budget.go` | orçamento, custo e classes de falha |
| `steps.go` | atualizações das etapas e do log, sempre sob a trava |
| `discovery.go` | discovery do repositório e grafo |
| `promotion.go`, `reconcile.go`, `review_scm.go` | o que entra na base, por repositório, e o PR |
| `sequence.go`, `streams.go`, `candidates.go` | ondas de US, frentes paralelas e best-of-N |
| `change.go`, `acceptance.go`, `specialties.go`, `perfcheck.go` | a prova de cada tipo de mudança, QA funcional, especialidades e desempenho |
| `risk.go`, `readiness.go`, `baseline.go`, `needs.go` | risco, prontidão, linha de base e o motivo de cada etapa |
| `manifest.go`, `journal.go`, `delivery.go`, `metrics.go`, `provenance.go` | manifesto e retomada, persistência, pacote de entrega, métricas e rastreio por chamada |

## `web/static`

`app.js` monta o painel. `evidence.js` tem os renderizadores puros da evidência de uma US (rastreabilidade,
aceitação funcional, especialidades), carregados antes do `app.js`. Novos blocos de interface que só
transformam dados em HTML devem entrar em arquivos assim, sem framework novo.

## Próximas limpezas

A *Saúde do código* (`faztudo health areas`, sem chamar modelo) aponta, nesta ordem,
`internal/pipeline`, `web/static` e `internal/workspace`, pelo que mais muda junto e pelos casos especiais
(esperas, novas tentativas, testes pulados). Cada limpeza:

1. roda com `faztudo health run --area <área>`, numa branch própria, sem mudar comportamento;
2. vai em PR próprio, separado de qualquer funcionalidade;
3. passa por revisão humana antes de entrar, além do CI.
