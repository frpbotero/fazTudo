# FazTudo

Orquestrador de agentes de IA para desenvolver software a partir de User Stories.

Você carrega um documento com User Stories (Markdown), aponta o repositório do projeto e o FazTudo
conduz cada história por um pipeline de agentes: verifica o que já existe, especifica, planeja,
implementa, roda os testes do projeto, revisa segurança e código, e documenta. Os agentes são os CLIs
**Claude Code**, **Codex** e **Gemini CLI** que você já tem instalados, com troca automática de um
para o outro quando um atinge o limite de uso.

Roda como aplicativo desktop (macOS, Windows e Linux) ou como servidor local com dashboard web.

> Sem os CLIs instalados o app funciona em **modo simulação** (padrão): percorre todo o fluxo sem
> gastar tokens nem alterar arquivos. Bom para conhecer a ferramenta.

---

## Sumário

- [Como funciona](#como-funciona)
- [Instalação](#instalação)
- [Primeiros passos](#primeiros-passos)
- [Fluxos de agentes](#fluxos-de-agentes)
- [Pendências](#pendências)
- [Retomar uma execução](#retomar-uma-execução)
- [Configurações](#configurações)
- [Consumo de tokens](#consumo-de-tokens)
- [Várias janelas](#várias-janelas)
- [Modo servidor](#modo-servidor)
- [Modo console](#modo-console)
- [Desenvolvimento](#desenvolvimento)
- [Estrutura do repositório](#estrutura-do-repositório)
- [Documentação detalhada](#documentação-detalhada)

---

## Como funciona

```text
Discovery  →  Passo 0  →  Especificação  →  Arquiteto  →  Frontend / Backend
                                                              │
        Documentação  ←  E2E  ←  Code review  ←  Segurança  ←  Verificação executável
```

| Etapa | O que faz |
|-------|-----------|
| **Discovery** | Mapeia o repositório (stacks, comandos, arquitetura, convenções). Feito uma vez e reaproveitado nas execuções seguintes. |
| **Passo 0** | Confere, critério a critério, o que o código já implementa. Se a US já está atendida, é encerrada sem implementar nada; se depende de algo que falta, é bloqueada com o motivo. |
| **Especificação** | Transforma a US em contratos e em um plano de testes com um ID por caso. |
| **Arquiteto** | Escolhe o padrão de projeto, quais agentes participam e quais frentes de trabalho podem rodar em paralelo. |
| **Frontend / Backend** | Implementam o que falta, seguindo o plano e as regras do projeto. |
| **Verificação executável** | O próprio FazTudo roda os comandos do projeto (testes, lint, build) e devolve ao dev o que falhou. |
| **Segurança** | Revisão OWASP, somente leitura. |
| **Code review** | Revisores em paralelo: aderência à especificação, simplicidade, testes e interface. |
| **E2E** | Testes ponta a ponta com Playwright. |
| **Documentação** | Guia de uso e/ou documentação de manutenção, quando a mudança pede. |

Pontos que valem saber:

- **Portões de qualidade.** Segurança, review e E2E devolvem um veredito. Se houver problemas, o dev
  recebe exatamente o que corrigir e o portão roda de novo. Depois de algumas rodadas sem aprovação, a
  US segue como *Com pendências* e a execução continua. Nunca trava por um problema teimoso.
  Resposta sem veredito válido não aprova: a US fica com validação *inconclusiva*, mostrada à parte
  do resultado da execução.
- **Git por US.** Cada US ganha uma branch `faztudo/<us>-<data>` e cada etapa vira um commit, com o
  diff visível no painel. O trabalho só entra na sua branch base por merge ao fim da onda.
- **Paralelo ou sequencial.** US independentes e frentes independentes de uma US podem rodar juntas,
  cada uma em um worktree git próprio. Também dá para rodar tudo em sequência.
- **Somente leitura onde importa.** Discovery, Passo 0, especificação, arquiteto, segurança e review
  rodam em modo que o CLI não consegue editar arquivos.
- **Arquivos de entrada protegidos.** Os `.md` das US e EDDs não podem ser alterados pelos agentes.
  Se um for, o conteúdo é restaurado e a etapa falha.

---

## Instalação

### Requisitos

- **Go 1.22+** para compilar.
- Para execução real: [`claude`](https://docs.claude.com/claude-code),
  [`codex`](https://github.com/openai/codex) e/ou [`gemini`](https://github.com/google-gemini/gemini-cli)
  no `PATH`, já autenticados (Gemini: `npm i -g @google/gemini-cli` e `gemini` uma vez para entrar com a conta Google).
- **git** (o FazTudo inicializa o repositório se ele ainda não for um).
- Opcional: [graphify](https://github.com/safishamsi/graphify) (`uv tool install graphifyy`) para o
  grafo do código, que reduz a varredura de arquivos pelos agentes.
- Opcional: as skills de [agent-skills](https://github.com/addyosmani/agent-skills)
  (`npx skills add addyosmani/agent-skills -g`), usadas pelas etapas quando instaladas.

### macOS (app desktop)

```bash
./start.sh
```

Compila e instala em `/Applications/FazTudo.app` (substituindo a cópia anterior) e abre o app.
**Feche o FazTudo antes de reinstalar.** Na primeira vez, o macOS pode bloquear o app por não ter
assinatura da Apple: clique com o botão direito em FazTudo.app → *Abrir*, ou rode
`xattr -dr com.apple.quarantine /Applications/FazTudo.app`.

### Linux

```bash
./start.sh
```

Compila e executa `build/bin/FazTudo`. Precisa de `libgtk-3-dev` e `libwebkit2gtk-4.1-dev`.

### Windows

Use o instalador `FazTudo-<versão>-windows-amd64-setup.exe` da página de Releases do repositório
(o SmartScreen pede *Mais informações → Executar assim mesmo*), ou o [modo servidor](#modo-servidor).

### Instaladores prontos

Ao publicar uma tag (`git tag v0.2.0 && git push origin v0.2.0`), o workflow
[desktop-release](.github/workflows/desktop-release.yml) gera o `.dmg` (macOS), o `.exe` (Windows) e
`.deb`/`.rpm`/`.tar.gz` (Linux) e os anexa à Release.

---

## Primeiros passos

1. **Abra o app** e vá em **Repositório**. Informe a pasta do projeto onde os agentes vão trabalhar.
   O FazTudo detecta as stacks e os comandos de teste.
2. Em **Provedores**, escolha o provedor padrão (Claude Code, Codex ou Gemini), se o failover fica ativo e os
   modelos de cada nível (*heavy* para arquitetura, segurança e review; *standard* para o resto).
3. Em **Repositório → Portões de qualidade → Editar**, ajuste o que valer para o projeto: comandos de
   verificação, arquivos protegidos, git, desenvolvimento paralelo ou sequencial e o
   [fluxo de agentes](#fluxos-de-agentes).
4. Em **Nova execução**, abra o `.md` com as User Stories (ou cole o texto). Cada seção `US-101`,
   `HU 3`, `User Story …` ou `História …` vira uma US; o texto antes da primeira vira contexto comum.
5. Escolha entre **desenvolvimento contínuo** (todas as US, na ordem do documento) ou **apenas uma US**,
   decida se é **simulação** ou **execução real** e inicie.
6. Acompanhe em **Execução**: etapas, saídas dos agentes, commits, diffs e o log em tempo real.

> Comece em simulação para ver o fluxo. A execução real deixa os agentes editar arquivos e rodar só os
> comandos de verificação e os liberados na política, e roda esses comandos no shell: use em repositórios que você controla e com o git limpo
> (alterações pendentes em arquivos versionados impedem a execução, para os commits dos agentes não
> se misturarem com os seus).

---

## Fluxos de agentes

Nem todo trabalho precisa de todas as etapas. Em **Repositório → Portões de qualidade → Editar →
Fluxo de agentes** você escolhe o que roda, por repositório:

| Fluxo | O que roda | Quando usar |
|-------|-----------|-------------|
| **Completo** (padrão) | Todas as etapas. | Entrega com a máxima verificação. |
| **Equilibrado** | Tudo, menos segurança, E2E e documentação. | Mais rápido, mantendo a revisão de código. |
| **Rápido** | Passo 0, plano de testes, arquiteto, devs e verificação executável. | Iterar depressa. O Passo 0 continua evitando reimplementar o que já existe. |
| **Auditoria** | Verificação executável, segurança e code review. Sem Passo 0, arquiteto nem devs. | Revisar aplicações **já desenvolvidas**: roda os testes e revisa o código existente sem alterar nada. |
| **Personalizado** | Você liga e desliga cada etapa opcional. | Combinações específicas. |

Regras:

- O **arquiteto e os desenvolvedores sempre rodam**, exceto na Auditoria.
- Segurança, E2E e documentação, mesmo ligados, ainda dependem de o arquiteto julgá-los necessários
  para a US.
- Na **Auditoria** ninguém corrige o que encontra: cada problema vira uma [pendência](#pendências) com
  `arquivo:linha` e a correção sugerida. Para auditar, carregue as US que descrevem o que foi
  construído; cada uma é revisada separadamente.
- O fluxo vale por repositório. Depois de uma auditoria, volte a *Completo* antes de desenvolver.

O campo **Desenvolvimento** (*Paralelo* ou *Sequencial*) é independente do fluxo e pode ser combinado
com qualquer um.

---

## Pendências

Toda execução real que termina com algo não resolvido grava um relatório em Markdown:

```text
~/.config/faztudo/pendencias/<nome-do-repositório>-<hash>.md
```

O caminho completo aparece no log ao fim da execução. O arquivo fica **fora** do repositório de
propósito: um arquivo novo lá seria visto como alteração pendente sua e atrapalharia a execução
seguinte.

Entram no relatório:

- problemas que segurança, review ou E2E não conseguiram ver corrigidos nas rodadas de correção, com
  severidade e local;
- verificações do projeto que falharam e achados de análise estática;
- etapas com falha ou canceladas;
- US bloqueadas no Passo 0 ou interrompidas. Uma US que o código já atende **não** é pendência.

Execuções em simulação não geram o arquivo. Uma execução limpa apaga o relatório da anterior.

---

## Retomar uma execução

A última execução de cada repositório é salva enquanto roda (em `~/.config/faztudo/runs/`) e volta à
tela ao abrir o app ou trocar de repositório. Se ela parou (falha, **Parar**, app fechado ou travado),
o botão **Continuar de onde parou** retoma na etapa marcada *Parou aqui*, na branch da US, reaproveitando
tudo que já foi concluído: as US entregues não são refeitas e o discovery é reaproveitado.

Se uma etapa falhou, **Refazer etapa** repete só ela. A retomada refaz a etapa inteira onde parou
(por exemplo, o QA E2E completo, mesmo que o que estivesse rodando fosse a segunda rodada de correção).

---

## Configurações

| Onde | O que |
|------|-------|
| **Provedores** | Provedor padrão, failover, modelos e nível de esforço por provedor. A lista do Codex vem do catálogo do CLI instalado. |
| **Repositório → Branch base** | Branch de desenvolvimento de cada repositório (inclusive repositórios aninhados). Toda execução troca para ela antes de criar as branches `faztudo/`. |
| **Repositório → Portões de qualidade** | Comandos de verificação (detectados ou manuais), arquivos protegidos, limite de linha, tempo limite, git, paralelo ou sequencial, candidatos por implementação (best-of-N), fluxo de agentes e limites dos agentes (turnos, parar ao esgotar, comandos de shell liberados). |

Tudo é salvo a cada alteração em `~/.config/faztudo/config.json` (permissão `0600`), e as políticas de
qualidade valem por repositório. Flags e variáveis de ambiente explícitas têm prioridade sobre o
arquivo.

Limites dos agentes (em **Portões de qualidade**, campo do `config.json` entre parênteses):

| Campo | Padrão | O que faz |
|-------|--------|-----------|
| Turnos (`max_turns`) | `0` (60 em etapas que escrevem, 30 nas de leitura) | Turnos por chamada do Claude (`--max-turns`). Leitura recebe metade. |
| Parar ao esgotar (`stop_on_turn_limit`) | desligado | Encerra a execução quando um agente esgota os turnos; desligado, segue e registra pendência. |
| Comandos liberados (`agent_commands`) | `npx playwright test`, `npx playwright install chromium` | Comandos que os agentes podem rodar além dos de verificação (ex.: `npm install`). |

---

## Consumo de tokens

Cada etapa é uma chamada headless nova, sem `--resume`, com só o contexto de que precisa:

- **Contexto fixo primeiro.** Repositório, discovery, instruções, regras e a US abrem todo prompt da
  US, na mesma ordem; o papel da etapa vem no fim. O cache de prompt dos CLIs reaproveita esse início.
- **Só as entradas da etapa.** Implementadores recebem Passo 0, especificação e plano; revisores recebem
  especificação, plano e implementação. Relatórios de teste não seguem adiante.
- **Testes fora do LLM.** O FazTudo roda testes, lint e build e devolve ao agente só o resumo da falha
  (teste, mensagem, até 50 linhas).
- **Ambiente mínimo.** O Claude roda com `--safe-mode` (sem skills, plugins, hooks, MCP nem CLAUDE.md do
  usuário; o login continua valendo) e só as ferramentas da etapa; o Codex não relê o `AGENTS.md`.
- **Spec Kit no repositório alvo.** Se ele tem `.specify/memory/constitution.md` ou `specs/*/`, os
  agentes recebem só os caminhos (constituição e features com `spec.md`, `plan.md`, `tasks.md`) e leem
  a feature da US; quem implementa segue `tasks.md` e marca `[X]`.
- **Limites.** `--max-turns` em cada chamada do Claude e até 2 ou 3 rodadas de correção por portão.
- **Modelo por nível.** Claude: Opus no *heavy* (arquitetura, segurança), Sonnet no *standard*, Haiku no
  *light*. Ajuste em **Provedores**.

Cada chamada real grava uma linha em `~/.config/faztudo/logs/tokens.jsonl`: agente, modelo, US, etapa,
tentativa, tokens de entrada (com os de cache), saída e custo (o Claude informa; Codex e Gemini, só tokens).
Para somar por execução:

```bash
cd llm-orchestrator && go run . -tokens     # ou ./bin/faztudo -tokens
```

---

## Várias janelas

Para tocar dois projetos ao mesmo tempo, use **Arquivo → Nova janela** (⌘N / Ctrl+N). Cada janela
é uma instância do app, com sua execução e seu repositório. No mesmo repositório só uma janela executa
por vez. Configuração e discovery são compartilhados.

---

## Modo servidor

Sem janela nativa, com dashboard no navegador:

```bash
./start.sh --web          # compila e inicia em http://127.0.0.1:8081
```

ou, dentro de `llm-orchestrator/`:

```bash
make run                  # compila em bin/ e inicia
make dev                  # go run com logs em debug
make docker               # imagem distroless (modo simulação)
```

| Flag | Variável | Padrão | Descrição |
|------|----------|--------|-----------|
| `-addr` | `ADDR` / `PORT` | `127.0.0.1:8081` | Endereço HTTP |
| `-repo` | `REPO_PATH` | — | Repositório alvo inicial |
| `-provider` | `DEFAULT_PROVIDER` | `claude` | `claude`, `codex` ou `gemini` |
| `-config` | `FAZTUDO_CONFIG` | `~/.config/faztudo/config.json` | Arquivo de configuração (`""` desativa) |
| `-dry-run` | `DRY_RUN` | `true` | Começa em simulação |
| `-log-level` | `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `-tokens` | — | — | Soma o uso de tokens por execução e sai |

> **Segurança.** Em execução real os agentes editam arquivos do repositório e rodam os comandos
> liberados (Claude: `--permission-mode dontAsk` com `--allowedTools`; Codex: `--sandbox workspace-write`;
> Gemini: `--approval-mode auto_edit`), e os comandos de verificação rodam no shell. Por isso o servidor escuta só em `127.0.0.1` e recusa escritas vindas de
> outras origens. Não exponha a porta em redes não confiáveis.

---

## Modo console

O mesmo binário também funciona sem janela e sem iniciar o servidor web:

```bash
cd llm-orchestrator
go run . run --repo /caminho/do/projeto --file /caminho/US.md
go run . run --repo /caminho/do/projeto --file /caminho/US.md --stories US-1,US-3 --real
go run . tui --repo /caminho/do/projeto
```

`run` usa simulação por padrão; `--real` executa os CLIs e pode alterar o repositório. A execução
permanece no terminal e Ctrl+C solicita parada. De outro terminal, `status`, `logs` e `stop` consultam
ou controlam a execução ativa pela configuração local, sem abrir uma porta. `resume` continua a última
execução interrompida e `retry --story 0 --step review` refaz uma etapa com falha. Use `--config` para
escolher o mesmo arquivo de configuração em todos os comandos.

`faztudo help` lista os comandos para repositório, política de qualidade, provedores, modelos,
prompts, branches e diffs. As políticas e configurações podem ser exportadas com `show` e aplicadas
de um arquivo JSON com `apply --file`. Na TUI, Tab troca a seção e a linha `>` executa os mesmos
comandos da CLI; caminhos com espaços podem ficar entre aspas.

---

## Desenvolvimento

Todo o código está em [`llm-orchestrator/`](llm-orchestrator/):

```bash
cd llm-orchestrator
make test          # testes com -race
make lint          # gofmt + go vet
make cover         # cobertura
make desktop-dev   # janela desktop com recarga dos assets
make install-mac   # compila e instala o app no macOS
make dmg           # gera o instalador .dmg
```

Backend em Go; interface em HTML, CSS e JS puros, embutidos no binário; app desktop com
[Wails](https://wails.io) (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0`).

---

## Estrutura do repositório

```text
start.sh                      # compila e abre o app (--web para o modo servidor)
llm-orchestrator/
  main.go, desktop*.go        # entrada: servidor e app Wails
  internal/provider/          # CLIs (Claude, Codex, Gemini), uso de tokens, parser de quota e failover
  internal/pipeline/          # etapas, prompts, ondas, frentes, review, best-of-N, retomada, pendências
  internal/workspace/         # repositório alvo, discovery, política de qualidade e fluxos
  internal/quality/           # comandos, sintaxe, legibilidade, rastreabilidade, arquivos protegidos
  internal/vcs/               # git: branches, commits por etapa e worktrees
  internal/stories/           # parser de Markdown em User Stories
  internal/settings/          # persistência da configuração
  internal/server/            # API JSON, SSE e middlewares
  internal/eval/              # benchmark de configurações
  web/static/                 # dashboard embutido
  evals/                      # suítes de avaliação
.github/workflows/            # build dos instaladores
```

---

## Documentação detalhada

- [`llm-orchestrator/README.md`](llm-orchestrator/README.md): referência completa do pipeline, ondas e
  paralelismo, portões de qualidade, persistência, app desktop e tabela da API HTTP.
- [`llm-orchestrator/evals/README.md`](llm-orchestrator/evals/README.md): como comparar configurações
  do pipeline com `faztudo -eval`.
- [`blueprint_e_guia_de_constru_o_orquestrador_de_agentes_llm.md`](blueprint_e_guia_de_constru_o_orquestrador_de_agentes_llm.md):
  blueprint original e próximos passos.
