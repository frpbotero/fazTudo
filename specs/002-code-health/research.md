# Pesquisa: Saúde do código

## R1. Execução de limpeza no orquestrador

- **Decisão**: novo ponto de entrada `Orchestrator.StartCleanup` em `internal/pipeline/cleanup.go`, que reaproveita
  bloqueio do repositório, `prepare` (política, guard de entrada, git, base), `updateGraph`, `discover` (cache),
  `runStage`/`executeRetrying` (failover, retries, tokens) e `guardInputs`. O `Run` ganha `Kind` e `Cleanup`; a
  limpeza aparece como uma única `StoryRun` com três etapas: linha de base, limpeza e verificação.
- **Por quê**: a interface, o console, o journal e os logs já sabem mostrar uma `StoryRun` com etapas; nada de
  componente novo para acompanhar progresso. O fluxo de US (análise, ondas, gates, merge nas bases) não serve:
  a limpeza não pode fazer merge (RF-003) e não tem US.
- **Alternativas**: (a) US sintética no pipeline normal: herdaria merge nas bases, spec e arquiteto; rejeitada.
  (b) Processo separado fora do orquestrador: duplicaria lock, failover e registro de uso; rejeitada.

## R2. Linha de base e comparação

- **Decisão**: rodar todos os comandos da política (`ss.checks`, sem parar na primeira camada) antes e depois,
  com `o.runCheck`. Cada comando vira um conjunto de fingerprints (`quality.ParseFailures` +
  `quality.Fingerprints`; sem falha reconhecida, `quality.TextFingerprint` do nome do comando). Piorou se:
  um fingerprint novo aparece, ou um comando que passava passa a falhar ou estourar o tempo.
- **Por quê**: os fingerprints já identificam falhas herdadas entre US (`repair.go`); a mesma identidade separa
  falha antiga de falha nova (caso de borda "linha de base já falhando").
- **Alternativas**: comparar só contagem de falhas: uma falha trocada por outra passaria; rejeitada.

## R3. Proteção de testes com ID de critério de aceite

- **Decisão**: antes e depois, varrer arquivos de teste (`quality.IsTestFile`) e coletar os IDs no formato dos
  critérios (o mesmo padrão de `quality.ExtractRequirements`, ex.: `UT-CART-01`). ID que some reprova.
  Arquivos de entrada e protegidos usam o `Guard` existente (restaura e reprova).
- **Por quê**: determinístico, independe de linguagem, e é o vínculo que o FazTudo já usa para rastrear testes.

## R4. Escopo da área

- **Decisão**: área = caminho relativo de diretório existente dentro do repositório (`.` = repositório inteiro,
  só por escolha explícita). Arquivo alterado fora da área reprova a execução. O prompt diz que mudança de
  interface que afete quem está fora vira apontamento no registro, não alteração.
- **Por quê**: a spec define área como o que a limpeza pode mudar; e "contratos públicos ficam" impede
  mudanças que obrigariam tocar chamadores de fora.

## R5. Indicadores sem LLM

- **Decisão**: pacote novo `internal/health`.
  - **Frequência de mudança**: `git log -n 500 --format=%x00 --name-only` (últimos 500 commits), contagem por
    arquivo, somada por área.
  - **Mudam juntos**: pares de arquivos em commits com até 20 arquivos; par com ≥ 3 commits juntos e ≥ 60% de
    coocorrência conta para as áreas dos dois; par entre áreas diferentes conta em dobro (ligação escondida).
  - **Ciclos**: do `graph.json` do graphify (formato node-link: `nodes[].source_file`, `links[]` com
    `relation` `calls`/`references`), arestas entre arquivos agregadas por diretório; componentes fortemente
    conexos (Tarjan) com mais de um diretório marcam ciclo nas áreas envolvidas. Sem grafo: indicador fora.
  - **Casos especiais**: regex por linguagem em arquivos de código da área: `sleep` (`time.Sleep`,
    `setTimeout(`, `sleep(`, `Thread.sleep`), `retry`, teste pulado (`t.Skip`, `.skip(`, `xit(`, `xdescribe(`,
    `@Disabled`, `@pytest.mark.skip`), exceção engolida (`catch (…) {}`, `except…: pass`, `_ = err`).
  - **Área candidata**: a partir da pasta comum a todo o código (ignorando arquivos soltos na raiz e pastas
    que começam com `.`), as duas pastas seguintes (`base/a/b`) ou só a primeira (`base/a`). Arquivo solto
    direto na pasta comum não tem área: limpar a pasta comum seria limpar tudo. Ignora o que o walker de
    `quality` já ignora (`.git`, `node_modules`, `vendor`, build) mais `graphify-out` e `faztudo-out`.
    Medido no próprio FazTudo: `llm-orchestrator/internal/pipeline` em primeiro, em 0,25 s.
  - **Pontuação**: cada indicador normalizado pelo máximo entre as áreas (0–1), pesos iguais; top 3 com os
    indicadores que somaram mais.
- **Por quê**: princípio V (determinístico fora do LLM); tudo vem de git e do grafo que o FazTudo já gera.
- **Desempenho**: 500 commits limita o custo do co-change (pares por commit ≤ 190); a meta é < 30 s (CS-001).

## R6. Agente da limpeza

- **Decisão**: etapa `CleanupStage` (Tier pesado, escrita), skill `structural-health` (modo limpeza) e
  `incremental-implementation`. Uma chamada por execução; `Rodadas` (padrão 3, máx. 5) vai no prompt como limite
  da skill; turnos pelo limite de escrita da política, com o tratamento de turno esgotado já existente.
  Comandos permitidos: os da política (como implementadores) mais os `safeCommands` (inclui `git log`).
- **Registro de decisões**: o agente escreve em `faztudo-out/saude-<timestamp>.md` dentro do repositório
  (excluído do git via `.git/info/exclude`, como `graphify-out`). O orquestrador lê, guarda no `Run`
  (truncado) e copia para a pasta do journal.
- **Por quê**: o agente só escreve dentro do repositório; manter o arquivo fora do commit evita poluir a branch.

## R7. Branch e estado final

- **Decisão**: exige `Policy.Git`; sem git, a limpeza é recusada. Branch `vcs.BranchName("saude-"+área, agora)`
  a partir da base da política; um commit `faztudo(saude): <área>` ao fim; depois volta para a branch de base.
  Sem merge. Reprovada: a branch fica para inspeção.
- **Por quê**: RF-003 e premissa "resultado é branch para revisão".

## R8. Simulação

- **Decisão**: em simulação o agente é o `SimulatedRunner` e os comandos não rodam (como nas US); a
  verificação registra "simulação: comandos não executados" e aprova. Testes injetam `runCheck` e executor falso.

## R9. Retomar e refazer

- **Decisão**: execução de limpeza não é retomável na v1: `Resume`/`RetryFailed` recusam com mensagem pedindo
  nova limpeza. O journal guarda o resultado para exibição.
- **Por quê**: limpeza curta e de uma etapa; retomada exigiria reconstruir a linha de base.
