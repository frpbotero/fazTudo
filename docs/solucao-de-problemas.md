# Solução de problemas

| Sintoma | Causa provável | O que fazer |
|---|---|---|
| "outra janela do FazTudo já está executando neste repositório" | outra janela ou console roda no mesmo repositório | espere terminar ou pare a outra execução |
| A execução não começa: "nenhum provider disponível atende às exigências" | `strict_commands` ligado e só o Codex instalado, ou nenhum CLI no PATH | instale o Claude/Gemini CLI ou desligue `strict_commands` |
| Critérios aparecem como "só código de saída" | nenhum comando declara `report` | configure o relatório por caso (veja [configuracao.md](configuracao.md)) |
| Critério "sem relatório" | o arquivo do relatório não existe, é anterior ao comando ou é ilegível | confira `report.path`, relativo à raiz do repositório |
| Bug com reprodução "inconclusiva" | os testes novos falham só por compilação ou dependência ausente na worktree | escreva o teste para falhar por comportamento; confira se as dependências existem no repositório |
| Verificação das migrations ou desempenho "inconclusivo" | faltam `migration_checks` ou `benchmarks` | configure os comandos do projeto |
| Benchmark "inconclusivo" por ruído | a máquina variou mais que `max_noise_pct` | aumente `repeat`, feche processos pesados ou suba o limite com cuidado |
| "Orçamento da execução atingido" | um limite de `budget` foi alcançado | aumente o limite e use *Continuar de onde parou* |
| Integração parcial | o merge entrou num repositório aninhado e falhou no principal | resolva o conflito na base e use *Reconciliar integração* |
| PR não abre | `gh` sem login, remoto não é GitHub ou há repositórios aninhados | `gh auth status`; Bitbucket ainda não é suportado |
| Retomada refaz mais do que o esperado | o manifesto mudou (política, prompts, documento, branch) | o motivo está no log e em `resume_checks` |
| Etapa "não aplicável" que você esperava ver | o fluxo a desligou ou o arquiteto não a escolheu | `faztudo why --story N` mostra o motivo de cada etapa |

Logs da execução ficam no painel e em `faztudo logs`; o pacote de entrega em `entregas/` traz a
evidência completa de cada US.
