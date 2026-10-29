<!--
Sync Impact Report
- Version: template → 1.0.0 (first ratified version)
- Principles added: I Go simples e testado; II Português na interface; III Agentes seguros;
  IV Economia de contexto; V Determinístico fora do LLM
- Templates: plan/spec/tasks templates sem mudança necessária
- Deferred: nenhum
-->
# FazTudo Constitution

## Core Principles

### I. Go simples e testado
Código em `llm-orchestrator/` (Go 1.25, stdlib primeiro; UI em HTML/CSS/JS puros embutidos).
Antes de terminar: `make lint` (gofmt + go vet) e `make test` (go test -race ./...) passam.
Toda mudança de comportamento vem com teste no pacote (`*_test.go` ao lado). Comentários
explicam o porquê, em inglês, na densidade do código vizinho. Sem dependência nova sem motivo.

### II. Português na interface
Mensagens, logs da UI, prompts dos agentes e README em português do Brasil. Identificadores e
comentários de código em inglês.

### III. Agentes seguros
- Saída de agente nunca vira comando de shell; comandos vêm da política do repositório ou de
  listas fixas.
- Permissões mínimas por etapa: leitura onde só se lê; ferramentas listadas onde se escreve.
  Nenhuma flag que pule todas as confirmações sem decisão explícita do usuário.
- Sem credenciais em código, scripts, logs ou workflows. Servidor só em `127.0.0.1`.
- Arquivos de entrada (US, EDD) e protegidos nunca são alterados por agentes.

### IV. Economia de contexto (vale para todo agente: Claude, Codex, Gemini)
- Leia só os arquivos da tarefa atual. Use o grafo (`graphify query/explain/affected`) e busca
  pontual (`rg -n` em caminho específico) antes de abrir arquivos. Nada de varredura recursiva cega.
- Não releia arquivo que não mudou desde a última leitura na mesma sessão.
- Não despeje logs inteiros: `| tail -n 50`, filtre por falha (`-run TestX`, `--grep`), e cite
  só a linha decisiva.
- Resposta final curta: o que mudou, por arquivo, em uma linha cada.
- Uma tarefa por sessão; o contexto necessário vem no prompt, não da exploração.

### V. Determinístico fora do LLM
Testes, lint, build, formatação, busca de arquivos e parsing ficam no código do orquestrador.
O LLM recebe só o resumo das falhas (teste, mensagem, trecho do stack de até ~50 linhas).
Toda chamada de agente registra uso de tokens e custo quando o CLI informar.

## Fluxo de trabalho

Specs em `specs/` pelo Spec Kit (`/speckit.specify` → `/speckit.plan` → `/speckit.tasks` →
`/speckit.implement`). Commits pequenos, mensagem convencional (`feat:`, `fix:`, `refactor:`,
`docs:`, `chore:`). Branch por mudança; nada direto em `main`.

## Governance

Esta constitution prevalece sobre CLAUDE.md, AGENTS.md e GEMINI.md, que só apontam para cá e
guardam o que é específico de cada agente. Emenda: PR que altera este arquivo, com versão
semântica (MAJOR remove/redefine princípio, MINOR adiciona, PATCH esclarece) e Sync Impact Report
atualizado. Revisões de PR conferem aderência aos princípios.

**Version**: 1.0.0 | **Ratified**: 2026-10-21 | **Last Amended**: 2026-10-28
