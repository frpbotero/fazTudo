# Tarefas: FazTudo no console

**Entrada**: [spec.md](spec.md), [plan.md](plan.md)

## US1 — Executar histórias

- [X] T001 [US1] Despachar subcomandos sem alterar o modo servidor em `llm-orchestrator/main.go`.
- [X] T002 [US1] Executar e selecionar US, mostrar eventos e resultado em `llm-orchestrator/console.go`.
- [X] T003 [US1] Testar simulação, seleção e erros de entrada em `llm-orchestrator/console_test.go`.

## US2 — Gerenciar pelo console

- [X] T004 [US2] Expor consultas e alterações da API local em `llm-orchestrator/console_actions.go`.
- [X] T005 [US2] Controlar execução entre processos por arquivos privados em `llm-orchestrator/console_control.go`.
- [X] T006 [US2] Testar persistência, parada e retomada em `llm-orchestrator/console_test.go`.

## US3 — Interface interativa

- [X] T007 [US3] Construir seções, entrada, saída e rolagem em `llm-orchestrator/console_tui.go`.
- [X] T008 [US3] Testar navegação e parsing de comandos em `llm-orchestrator/console_tui_test.go`.

## Entrega

- [X] T009 Documentar uso em `README.md` e `llm-orchestrator/README.md`.
- [X] T010 Executar `make lint`, `make test` e compilação desktop.

**Ordem**: T001–T003 → T004–T006 → T007–T008 → T009–T010. A US1 é o primeiro marco; cada grupo pode ser verificado de forma independente.
