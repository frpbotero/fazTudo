# Capacidades dos provedores

O FazTudo não presume isolamento equivalente entre CLIs. Cada adaptador declara o que **ele**
aplica, do jeito que o orquestrador invoca o CLI
([`provider/cli.go`](../llm-orchestrator/internal/provider/cli.go)). Um recurso que o adaptador não
passa ao CLI não conta como capacidade. Um teste confere que os argumentos batem com o declarado.

| Capacidade | Claude Code | Codex CLI | Gemini CLI |
|---|:---:|:---:|:---:|
| Somente leitura | sim (`plan`, só ferramentas de leitura) | sim (sandbox `read-only`) | sim (`--approval-mode plan`) |
| Escrita | sim | sim (sandbox `workspace-write`) | sim (`auto_edit`) |
| Comandos restritos à lista da política | **sim** (`dontAsk` + `--allowedTools`) | **não**: roda qualquer comando dentro do sandbox | **sim** (`--allowed-tools`) |
| Diagnóstico (leitura + comandos listados) | sim | sim, dentro do sandbox de leitura | não: fica só leitura |
| Saída estruturada | JSON | JSON (eventos) | JSON |
| Uso de tokens | sim | sim | sim |
| Limite de turnos | sim (`--max-turns`) | não | não |
| Cancelamento | grupo de processos (Unix) | grupo de processos (Unix) | grupo de processos (Unix) |
| Isolamento | regras de permissão do CLI | sandbox do sistema operacional | modo de aprovação do CLI |

O painel de providers mostra essas garantias e a versão do CLI instalado, detectada na verificação
que antecede cada execução real.

## Verificação antes da execução (preflight)

Antes de uma execução real, o FazTudo confere os providers que ela pode usar (o padrão e, com
failover, os demais instalados):

- Nenhum provider instalado capaz de atender à política: a execução **não começa** e o motivo
  aparece (ex.: `Codex CLI sem restrição de comandos à lista da política`).
- O provider padrão não atende, mas outro atende: a execução começa, com aviso de que as tarefas
  vão para outro provider.
- Limite de turnos configurado e provider sem esse recurso, ou provider que não restringe comandos:
  aviso no log da execução.

Em simulação não há verificação: nenhum CLI é chamado.

## Política `strict_commands`

Opção “Exigir provider que restrinja os agentes que escrevem aos comandos listados”. Desligada por
padrão.

- **Ligada:** um agente que escreve só roda em provider com restrição de comandos. O Codex nunca
  recebe tarefa de escrita, nem em failover. Se só ele estiver disponível, a tarefa espera a
  liberação de outro provider ou falha com o motivo. Tarefas somente leitura seguem para qualquer
  provider.
- **Desligada:** o failover para um provider que não restringe comandos é permitido, mas nunca
  silencioso. O log registra a troca e o isolamento que passa a valer.
