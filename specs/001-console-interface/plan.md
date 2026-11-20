# Plano de implementação: FazTudo no console

**Branch**: `feat/console-interface` | **Data**: 2026-11-11 | **Spec**: [spec.md](spec.md)

## Resumo

Adicionar CLI e TUI ao binário atual sem novo servidor. Reutilizar os handlers da API em memória para manter os mesmos contratos, validações e persistência. A execução em primeiro plano mostra eventos do orquestrador; arquivos privados permitem comandos de outro processo.

## Contexto técnico

- **Linguagem**: Go 1.25.
- **Dependências**: stdlib e Bubble Tea v1/Lip Gloss para a TUI. Bubble Tea v2 requer Go 1.26.
- **Persistência**: `settings.Store` e journal existentes; controle ativo por arquivos locais `0600` em diretório `0700`.
- **Plataformas**: macOS, Linux e Windows; o build desktop não inclui os arquivos de console.
- **Testes**: testes Go do pacote principal, `make lint`, `make test` e compilação com tag `desktop`.

## Constituição

- Código no módulo Go existente; sem framework web ou serviço de rede novo.
- Mensagens em português; execução real explícita; saída de agentes não vira comando.
- Validações, testes e parsing permanecem determinísticos fora do LLM.
- Tests e lint obrigatórios antes da entrega.

## Interface de comandos

`run`, `stories parse`, `status`, `logs`, `stop`, `resume`, `retry`, `repo show/set/discovery-reset/branches/base`, `policy show/apply`, `config show/apply`, `providers list/exhaust/restore`, `prompts list/show/set/reset`, `commit show` e `tui`. `--config` aponta para o mesmo arquivo usado pelo painel; `--file -` lê stdin. Consultas estruturadas usam JSON e `status --json` entrega o snapshot completo.

## Fluxo de dados

1. O comando cria Router, Workspace, Store e Orchestrator como o servidor atual.
2. Chamadas locais passam pelo handler HTTP em memória; não há listener.
3. `run`, `resume` e `retry` aguardam o pipeline e mostram eventos. O journal mantém retomada.
4. Uma execução ativa publica presença temporária e processa pedidos privados de outros processos. A presença vencida é ignorada.
5. A TUI usa os mesmos comandos, sem invocar shell, e recebe linhas de saída por canal.

## Verificação

- Testar simulação, seleção de US, parsing, erro de arquivo, configuração e política persistidas.
- Testar status, cota, parada de outro comando e retomada.
- Testar navegação, parsing de caminhos com espaços, rolagem e limpeza da TUI.
- Rodar os gates do projeto e confirmar compilação desktop.
