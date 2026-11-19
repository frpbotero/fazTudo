# Tarefas: Saúde do código

**Entrada**: [spec.md](spec.md), [plan.md](plan.md), [research.md](research.md), [data-model.md](data-model.md),
[contracts/](contracts/), [quickstart.md](quickstart.md)

**Testes**: obrigatórios pela constitution (princípio I): toda mudança de comportamento vem com teste no pacote.
Caminhos relativos à raiz do repositório; código em `llm-orchestrator/`.

## Fase 1: Preparação

- [X] T001 Exportar `ExcludeFromGit(repo, pattern string)` (hoje `excludeFromGit`, usado para `graphify-out`) para também excluir `faztudo-out/`, ajustando o chamador em `llm-orchestrator/internal/workspace/graph.go`
- [X] T002 [P] Criar o pacote `llm-orchestrator/internal/health` com comentário de pacote em `llm-orchestrator/internal/health/areas.go` (tipos `AreaScore` e `AreaSuggestion` conforme [data-model.md](data-model.md), sem lógica ainda)

## Fase 2: Base (bloqueia todas as US)

- [X] T003 Adicionar `Run.Kind` (`"stories"` | `"cleanup"`; vazio = `stories`, compatível com journals antigos) e `Run.Cleanup *CleanupInfo`, e copiar `Cleanup` em `Snapshot`, em `llm-orchestrator/internal/pipeline/orchestrator.go`
- [X] T004 Definir `CleanupOptions`, `CleanupInfo` e `CheckSnapshot` conforme [data-model.md](data-model.md) e `validateCleanupOptions`: "caminho relativo de diretório existente no repositório; `.` só explícito; sem `..` nem absoluto"; `rounds` "1–5, padrão 3", em `llm-orchestrator/internal/pipeline/cleanup.go`
- [X] T005 Definir `CleanupStage` (ID `cleanup`, Tier pesado, escrita, `Skills: structural-health, incremental-implementation`, System e Task em pt-BR) e `CleanupVerifyStage`/`CleanupBaselineStage` só para rótulo das etapas, em `llm-orchestrator/internal/pipeline/cleanup.go` (como `IntegrationStage` em `sequence.go`)
- [X] T006 Incluir `CleanupStage` no catálogo de prompts no grupo "Saúde do código" em `llm-orchestrator/internal/pipeline/prompts.go` e atualizar `llm-orchestrator/internal/pipeline/testdata/prompts_default.golden` (só a etapa nova)
- [X] T007 Fazer `Resume` e `RetryFailed` recusarem `Kind = cleanup` com o erro "Limpeza não é retomável; inicie uma nova." em `llm-orchestrator/internal/pipeline/journal.go` e `llm-orchestrator/internal/pipeline/orchestrator.go`, com teste em `llm-orchestrator/internal/pipeline/cleanup_test.go`

**Checkpoint**: `make lint && make test` verdes; nada visível muda para execuções de US.

## Fase 3: US1 — Limpar uma área escolhida (P1) 🎯 MVP

**Objetivo**: limpeza de uma área em branch própria, com linha de base, agente, verificação e veredito.
**Teste independente**: limpeza simulada em repositório git temporário gera branch própria, registro e veredito
(acionada pelo orquestrador nos testes, sem console nem interface).

### Testes (escrever antes, devem falhar)

- [X] T008 [P] [US1] Testar `validateCleanupOptions` (área inexistente, absoluta, com `..`, vazia; `.` aceito só explícito; `rounds` negativo e 6 recusados, 0 (omitido) vira 3) em `llm-orchestrator/internal/pipeline/cleanup_test.go`
- [ ] T009 [P] [US1] Testar a comparação de linha de base: fingerprint novo reprova; comando que passava e passa a falhar ou estourar tempo reprova; falha igual à da linha de base aprova; arquivo fora da área reprova; ID de teste ausente reprova, em `llm-orchestrator/internal/pipeline/cleanup_verdict_test.go`
- [ ] T010 [P] [US1] Testar `TestIDs(root)`: coleta IDs de critério (ex.: `UT-CART-01`) só de arquivos de teste, em `llm-orchestrator/internal/quality/trace_test.go`
- [ ] T011 [P] [US1] Testar `ChangedFiles(ctx, base)` (arquivos alterados entre a base e o HEAD, incluindo não commitados) em `llm-orchestrator/internal/vcs/git_test.go`
- [ ] T012 [US1] Testar o fluxo com executor falso e `runCheck` injetado em `llm-orchestrator/internal/pipeline/cleanup_test.go`: simulação aprova; agente que apaga teste `UT-…`, mexe fora da área ou altera arquivo de entrada → reprovada com o motivo e branch mantida; falha nova em `runCheck` → reprovada; falha já na linha de base → aprovada; `Stop` no meio → cancelada e repositório de volta à base; política sem git, repositório ocupado ou árvore suja → recusada; registro de decisões lido do `faztudo-out/` e truncado em 64 KB

### Implementação

- [ ] T013 [P] [US1] Implementar `TestIDs(root string) []string` com o mesmo padrão de ID de `ExtractRequirements` e `IsTestFile` em `llm-orchestrator/internal/quality/trace.go`
- [ ] T014 [P] [US1] Implementar `ChangedFiles(ctx, base string) ([]string, error)` (`git diff --name-only base` mais não rastreados) em `llm-orchestrator/internal/vcs/git.go`
- [ ] T015 [US1] Implementar em `llm-orchestrator/internal/pipeline/cleanup_verdict.go`: `takeSnapshot` (todos os comandos da política via `o.runCheck`, sem parar por camada; fingerprints por `quality.ParseFailures`/`Fingerprints`, fallback `quality.TextFingerprint(nome)`), `compareSnapshots`, `outsideArea`, `missingIDs` e `cleanupVerdict` (motivos em pt-BR)
- [ ] T016 [US1] Implementar `buildCleanupPrompt` conforme [contracts/agent-prompt.md](contracts/agent-prompt.md) (contexto, instruções, skills, seção `## Limpeza` com modo, área, rodadas, caminho do registro, comentários/testes sim|não, protegidos, comandos; tarefa) em `llm-orchestrator/internal/pipeline/cleanup_prompt.go`
- [ ] T017 [US1] Implementar `StartCleanup(opts CleanupOptions) error` e a goroutine `runCleanup` em `llm-orchestrator/internal/pipeline/cleanup.go`: validação, `busyLocked`, `LockRepository`, `Run{Kind: cleanup}` com uma `StoryRun` "Saúde do código: <área>" e etapas `baseline`, `cleanup`, `cleanup-verify`; `prepare`, `updateGraph`, `discover`, branch `vcs.BranchName("saude-"+área, agora)`, `ExcludeFromGit(repo, "faztudo-out/")`, IDs de teste, linha de base, `runStage` com `CleanupStage` e Request `Tier: TierHeavy`, `guardInputs`, verificação, commit `faztudo(saude): <área>`, volta à base, `finish`
- [ ] T018 [US1] Ler `faztudo-out/saude-<timestamp>.md`, guardar em `CleanupInfo.DecisionLog` (até 64 KB) e copiar para a pasta do journal em `llm-orchestrator/internal/pipeline/cleanup.go`
- [ ] T019 [US1] Registrar em log os eventos da limpeza (início, branch, linha de base, veredito com motivos) em pt-BR, e incluir a limpeza reprovada em `renderPendencies`, em `llm-orchestrator/internal/pipeline/cleanup.go` e `llm-orchestrator/internal/pipeline/pendencies.go`

**Checkpoint**: T008–T012 verdes; US1 completa e testável sozinha.

## Fase 4: US2 — Ver onde vale limpar (P2)

**Objetivo**: até 3 áreas sugeridas por indicadores sem LLM.
**Teste independente**: repositório temporário com histórico roteirizado produz as áreas esperadas, na ordem
esperada, sem nenhuma chamada de CLI de IA.

### Testes

- [ ] T020 [P] [US2] Testar churn (últimos 500 commits) e co-change (commits com até 20 arquivos; par com "≥ 3 commits juntos e ≥ 60% de coocorrência"; par entre áreas conta em dobro) com repositório git temporário em `llm-orchestrator/internal/health/churn_test.go`
- [ ] T021 [P] [US2] Testar ciclos com `graph.json` mínimo (node-link, `nodes[].source_file`, `links[].relation` `calls`/`references`) com ciclo entre dois diretórios e sem ciclo em `llm-orchestrator/internal/health/cycles_test.go`
- [ ] T022 [P] [US2] Testar casos especiais por linguagem (`time.Sleep`, `setTimeout(`, `Thread.sleep`, `retry`, `t.Skip`, `.skip(`, `xit(`, `@Disabled`, `@pytest.mark.skip`, `catch (…) {}`, `except…: pass`, `_ = err`) em `llm-orchestrator/internal/health/special_test.go`
- [ ] T023 [P] [US2] Testar `Suggest`: top 3 com motivos, áreas de profundidade 1–2 com código, diretórios ignorados, sem grafo → aviso e sem `cycles`, histórico curto → aviso, em `llm-orchestrator/internal/health/areas_test.go`

### Implementação

- [ ] T024 [P] [US2] Implementar churn e co-change com `git log -n 500 --format=%x00 --name-only` em `llm-orchestrator/internal/health/churn.go`
- [ ] T025 [P] [US2] Implementar leitura do `graph.json`, grafo de diretórios e SCC (Tarjan) em `llm-orchestrator/internal/health/cycles.go`
- [ ] T026 [P] [US2] Implementar a tabela de regex por extensão e a contagem por área em `llm-orchestrator/internal/health/special.go`
- [ ] T027 [US2] Implementar `Suggest(ctx, repo, graph string) (AreaSuggestion, error)`: áreas candidatas (walker de `quality`), normalização pelo máximo (0–1), pesos iguais, top 3, `reasons` em pt-BR, avisos, em `llm-orchestrator/internal/health/areas.go`

**Checkpoint**: T020–T023 verdes.

## Fase 5: US3 — Escolher opções e disparar pelo console ou pela interface (P3)

**Objetivo**: mesmas escolhas e mesmo resultado no console, na TUI e no painel.
**Teste independente**: a mesma limpeza simulada disparada pelo console e pela API produz execuções equivalentes,
com as opções refletidas no prompt.

### Testes

- [ ] T028 [P] [US3] Testar no prompt: sem opções, comentários e testes fora do escopo; cada opção marcada aparece como `sim` e a outra como `não`, em `llm-orchestrator/internal/pipeline/cleanup_prompt_test.go`
- [ ] T029 [P] [US3] Testar `GET /api/health/areas` (200, 409 sem repositório) e `POST /api/health/start` (202, 400 área/rodadas inválidas, 409 ocupado/sem git), e resume/retry `409` em limpeza, conforme [contracts/http.md](contracts/http.md), em `llm-orchestrator/internal/server/server_test.go`
- [ ] T030 [P] [US3] Testar `health areas` (`--json`) e `health run` (simulação, códigos de saída 0/1/2, `--rounds` fora de 1–5) conforme [contracts/console.md](contracts/console.md) em `llm-orchestrator/console_test.go`

### Implementação

- [ ] T031 [US3] Adicionar rotas `GET /api/health/areas` e `POST /api/health/start` em `llm-orchestrator/internal/server/server.go`
- [ ] T032 [US3] Adicionar `health areas` e `health run` (acompanhamento em primeiro plano como `run`, Ctrl+C, `status`/`stop` de outro terminal) e a ajuda em `llm-orchestrator/console.go` e `llm-orchestrator/console_actions.go`
- [ ] T033 [US3] Aceitar `health areas` e `health run` na linha de comandos contextual da TUI em `llm-orchestrator/console_tui.go`, com teste em `llm-orchestrator/console_tui_test.go`
- [ ] T034 [US3] Criar a seção "Saúde do código" no painel (botão "Sugerir áreas", lista com motivos, campo de área, opções Comentários/Testes, rodadas, "Iniciar limpeza") e o cartão de resultado (veredito, motivos, branch, arquivos, registro de decisões) em `llm-orchestrator/web/static/index.html`, `llm-orchestrator/web/static/app.js` e `llm-orchestrator/web/static/app.css`

**Checkpoint**: T028–T030 verdes; quickstart de interface validado em simulação.

## Fase 6: Entrega

- [ ] T035 [P] Documentar a Saúde do código (sugestão, limpeza, proteções, comandos) em `llm-orchestrator/README.md`
- [ ] T036 Rodar `cd llm-orchestrator && make lint && make test` e `go build -tags desktop ./...`, e o roteiro simulado de [quickstart.md](quickstart.md)

## Dependências e ordem

- Fase 1 → Fase 2 → US1, US2 e US3.
- **US1** depende só da Fase 2.
- **US2** depende só da Fase 1 (T002); pode correr em paralelo com a US1.
- **US3** depende da US1 (`StartCleanup`) e da US2 (`Suggest`).
- Dentro de cada US: testes → implementação; T015 antes de T017; T013/T014 antes de T017; T024–T026 antes de T027.

## Paralelismo

- US1: T008, T009, T010 e T011 juntos; depois T013 e T014 juntos.
- US2: T020–T023 juntos; depois T024, T025 e T026 juntos.
- US3: T028, T029 e T030 juntos.
- US1 e US2 inteiras em paralelo após a Fase 2.

## Estratégia

1. **MVP**: Fases 1–3 (US1). A limpeza já funciona pelo orquestrador e é verificável por testes.
2. **+ US2**: sugestão de áreas.
3. **+ US3**: console, TUI e painel; a funcionalidade fica utilizável por pessoas.
4. Commit por fase, mensagem convencional (`feat:`), gates verdes a cada checkpoint.
