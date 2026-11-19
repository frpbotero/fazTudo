# Plano de implementação: Saúde do código

**Branch**: `feat/code-health` | **Data**: 2026-11-19 | **Spec**: [spec.md](spec.md)

## Resumo

Uma execução nova, separada do fluxo de US, que limpa a estrutura de uma área do repositório com o modo
limpeza da skill `structural-health`. O orquestrador sugere áreas por indicadores calculados sem LLM (git e
grafo do graphify), roda lint e testes antes e depois, e reprova por falha nova, arquivo fora da área, ID de
teste removido ou entrada alterada. O resultado é uma branch para revisão; nada é integrado à base.
Decisões em [research.md](research.md).

## Contexto técnico

- **Linguagem**: Go 1.25, stdlib; UI em HTML/CSS/JS puros embutidos (`web/static`).
- **Dependências novas**: nenhuma. Ciclos por Tarjan próprio sobre o `graph.json` (JSON node-link).
- **Persistência**: `Run` estendido no journal existente; registro de decisões copiado para a pasta do journal.
- **Testes**: `go test -race`, executor falso e `runCheck` injetado; repositórios git temporários para
  indicadores; golden de prompts atualizado com a etapa nova.
- **Plataformas**: macOS, Linux e Windows; servidor, console, TUI e desktop.
- **Restrições**: sugestão < 30 s em 5.000 commits (amostra de 500); sem chamada de modelo na sugestão.

## Constituição

| Princípio | Como o plano cumpre |
|---|---|
| I. Go simples e testado | Só stdlib; testes em cada pacote tocado; `make lint` e `make test` |
| II. Português na interface | Mensagens, logs, prompt e README em pt-BR; identificadores em inglês |
| III. Agentes seguros | Comandos só da política e `safeCommands`; Claude com `--safe-mode`; guard de entrada; área e IDs de teste verificados pelo orquestrador; servidor em 127.0.0.1 |
| IV. Economia de contexto | Agente restrito à área, grafo e discovery em cache no prompt, skills lidas por caminho |
| V. Determinístico fora do LLM | Indicadores, linha de base, comparação por fingerprints e veredito no orquestrador; uso de tokens registrado |

Sem violações. Reavaliado após o desenho: sem mudanças.

## Estrutura

```text
llm-orchestrator/
├── internal/health/            # novo: indicadores e sugestão de áreas (sem LLM)
│   ├── areas.go                #   áreas candidatas, pontuação, top 3
│   ├── churn.go                #   frequência de mudança e co-change (git log)
│   ├── cycles.go               #   graph.json → grafo de diretórios → SCC
│   └── special.go              #   regex de casos especiais por linguagem
├── internal/pipeline/
│   ├── cleanup.go              # novo: StartCleanup, linha de base, agente, veredito
│   ├── cleanup_prompt.go       # novo: buildCleanupPrompt
│   ├── stages.go               # CleanupStage (Tier pesado, escrita, skills)
│   ├── orchestrator.go         # Run.Kind, Run.Cleanup; Resume/Retry recusam limpeza
│   └── prompts.go              # grupo "Saúde do código" no catálogo
├── internal/server/server.go   # GET /api/health/areas, POST /api/health/start
├── console_actions.go          # health areas | health run
├── console_tui.go              # comandos na linha contextual
└── web/static/                 # seção "Saúde do código"
```

## Fluxo de dados

1. **Sugestão**: `health.Suggest(repo, graph)` → git log (500 commits) + `graph.json` + varredura de regex →
   `AreaSuggestion`. Servidor e console chamam direto, sem orquestrador.
2. **Início**: `StartCleanup(opts)` valida área e política (git obrigatório), bloqueia o repositório, monta o
   `Run` (`kind = cleanup`, uma `StoryRun`) e segue em goroutine, como `Start`.
3. **Preparação**: `prepare` (base, árvore limpa, guard), `updateGraph`, `discover` (cache), branch
   `saude-<área>`, IDs de teste coletados, `faztudo-out/` excluído do git.
4. **Linha de base**: todos os comandos da política via `o.runCheck` → `CheckSnapshot[]`.
5. **Agente**: `runStage` com `CleanupStage` e `buildCleanupPrompt` ([contrato](contracts/agent-prompt.md)).
6. **Verificação**: `guardInputs`; arquivos alterados (git) dentro da área; IDs de teste; comandos de novo;
   comparação de fingerprints → veredito e motivos.
7. **Fechamento**: commit `faztudo(saude): <área>`, registro de decisões lido e copiado, volta à branch de base,
   `finish` com sucesso (aprovada) ou falha (reprovada).

## Contratos

- [console.md](contracts/console.md): `health areas`, `health run`.
- [http.md](contracts/http.md): `GET /api/health/areas`, `POST /api/health/start`, mudanças em status/stop/resume.
- [agent-prompt.md](contracts/agent-prompt.md): montagem do prompt.
- [data-model.md](data-model.md): `CleanupOptions`, `AreaScore`, `CleanupInfo`, `CheckSnapshot`, transições.

## Verificação

- `internal/health`: repositório temporário com commits roteirizados → churn, co-change e top 3 esperados;
  `graph.json` mínimo com ciclo entre dois diretórios; regex por linguagem; sem grafo → aviso.
- `internal/pipeline`: limpeza simulada aprova; executor falso que apaga teste `UT-…`, mexe fora da área ou
  altera entrada → reprovada com o motivo; `runCheck` com falha nova → reprovada; falha já na linha de base →
  aprovada; Stop no meio → cancelada, base restaurada; Resume recusa; prompt com área, opções e skills.
- `internal/server` e console: códigos de status e saídas dos contratos; TUI aceita os comandos.
- Golden de prompts atualizado só com a etapa nova.
- [quickstart.md](quickstart.md) para a validação manual.

## Fora do escopo

Disparo automático ao fim de épico; merge da limpeza; retomada de limpeza interrompida; checagem automática de
contratos públicos além de arquivos de entrada e protegidos.
