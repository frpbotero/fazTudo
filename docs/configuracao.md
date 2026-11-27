# Configuração da política

A política fica por repositório no `config.json` do FazTudo (painel → *Portões de qualidade*,
`faztudo policy show | apply --file politica.json`, ou `PUT /api/workspace/policy`). Todo campo é
opcional: o que faltar usa o padrão. Exemplos prontos em [exemplos/](exemplos/).

## Verificação

| Campo | Padrão | O que faz |
|---|---|---|
| `auto_checks` | `true` | usa os comandos detectados nos manifestos |
| `checks` | `[]` | comandos `{name, run, report?, resource?}` quando `auto_checks` é `false` |
| `checks[].report` | — | relatório por caso: `{"format": "go-json"}`, `{"format": "junit", "path": "…"}` ou `{"format": "playwright-json", "path": "…"}` |
| `checks[].resource` | — | recurso compartilhado (`postgres`): comandos com o mesmo nunca rodam juntos |
| `check_timeout_s` | `600` | tempo limite de cada comando |
| `max_line_length` | `160` | limite de linha das checagens estáticas |
| `protected` | `US*.md`, `EDD*.md`… | arquivos de entrada que nenhum agente altera |
| `skip_baseline` | `false` | não roda a linha de base antes da US |
| `migration_checks` | — | comandos de migration em banco descartável |
| `benchmark_checks` | — | saída bruta lida pelo especialista de desempenho |
| `benchmarks` | — | linha de base × candidato; veja o README do orquestrador |

## Fluxo e risco

| Campo | Padrão | O que faz |
|---|---|---|
| `flow` | `full` | `full`, `balanced`, `fast`, `audit` ou `custom` |
| `disabled_stages` | — | etapas desligadas no fluxo `custom` |
| `risk_rules` | regras padrão | caminhos, palavras-chave, nível e etapas obrigatórias; `[]` desliga |
| `profiles` | — | perfis de stack: `go`, `angular-nestjs-postgres` |
| `style_blocking` | `false` | estilo apontado na revisão bloqueia a entrega |
| `max_fix_rounds` | `0` (2) | rodadas de correção por portão |
| `max_turns` | `0` | limite de turnos por chamada (Claude) |
| `stop_on_turn_limit` | `false` | turno esgotado para a execução |

## Paralelismo, orçamento e custo

| Campo | Padrão | O que faz |
|---|---|---|
| `sequential` | `false` | US e frentes uma de cada vez |
| `parallel_lenses` | `false` | revisores em paralelo |
| `max_parallel_reviews` | `0` (3) | revisores ao mesmo tempo |
| `max_parallel_agents` | `0` (4) | chamadas de agente ao mesmo tempo, na execução inteira |
| `candidates` | `1` | best-of-N; só com avaliação mostrando ganho |
| `budget` | — | `{max_minutes, max_calls, max_tokens, max_retries, max_retry_wait_s}` |
| `prices`, `prices_version` | — | US$ por milhão de tokens por modelo (`input`, `cached_input`, `output`), para estimar custo; sem `cached_input`, a entrada lida do cache custa o preço de `input` |
| `context_bytes` | `0` (4000) | tamanho de cada artefato repassado entre etapas |
| `log_limit` | `0` (500) | mensagens de log guardadas por execução |

## Integração e entrega

| Campo | Padrão | O que faz |
|---|---|---|
| `git` | `true` | branch por US, commit por etapa |
| `base_branches` | branch atual | base por repositório (`.` e aninhados) |
| `promote_unverified` | `false` | integra US sem comando executado |
| `strict_commands` | `false` | agente que escreve só em provider que restringe comandos |
| `agent_commands` | Playwright | comandos extras liberados aos agentes |
| `ephemeral_stacks` | `*-e2e-*` | stacks Docker de teste removidas no início e no fim |
| `scm` | local | `{"mode": "pr", "review_from": "alto", "draft": true}` abre PR em vez de integrar |
| `review_provider` | — | provider preferido para as revisões |
| `delivery_retention` | `0` (10) | pacotes de entrega guardados por repositório |

Valores fora do intervalo são recusados com a mensagem do campo; nada é aplicado pela metade.
