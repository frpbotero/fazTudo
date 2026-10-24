# FazTudo — Orquestrador de agentes

Servidor Go com dashboard web que conduz uma User Story por um pipeline de agentes LLM
(Passo 0 → especificação EDD/TDD → arquitetura → frontend/backend em frentes paralelas →
verificação executável → segurança → code review → E2E → documentação),
usando os CLIs **Claude Code** e **Codex** com failover automático quando um deles atinge o limite de uso.

## Repositório e discovery

Os agentes trabalham dentro de um **repositório local** configurado no painel (ou via `-repo`).
A primeira execução da sessão começa por um **discovery**: uma varredura dos manifestos
(`go.mod`, `package.json`, `pyproject.toml`, `pom.xml`, Dockerfile etc.) identifica as stacks por
diretório e um agente complementa com versões, comandos de build/teste, arquitetura e convenções.

O relatório é salvo em `~/.config/faztudo/discovery/` (um arquivo por repositório) e injetado como
contexto em todos os agentes. As execuções seguintes, mesmo depois de reiniciar o servidor,
**reutilizam o discovery** sem refazê-lo. Ele é refeito apenas ao clicar em **Refazer**.

**Grafo do código.** Com o [graphify](https://github.com/safishamsi/graphify) instalado
(`uv tool install graphifyy`), cada execução roda `graphify update .` no repositório antes do discovery
e de novo após integrar cada onda. É incremental, local (tree-sitter) e sem LLM: segundos. O grafo
fica em `graphify-out/` (fora do git via `.git/info/exclude`); o discovery recebe o `GRAPH_REPORT.md`
e todo agente recebe o caminho do `graph.json` para consultar (`graphify query/explain/affected`)
antes de varrer arquivos. Sem graphify, o pipeline segue sem o grafo (aviso no log).

**Skills.** Cada etapa nomeia as skills de [agent-skills](https://github.com/addyosmani/agent-skills)
que deve seguir (especificação → `spec-driven-development`; arquiteto → `planning-and-task-breakdown`,
`api-and-interface-design`; frontend/backend → `incremental-implementation`, `test-driven-development`
e `frontend-ui-engineering`/`api-and-interface-design`; segurança → `security-and-hardening`; code review →
`code-review-and-quality`, `code-simplification`; docs → `documentation-and-adrs`; rodadas de correção →
`debugging-and-error-recovery`, `test-driven-development`). Só entram no prompt as instaladas em
`~/.claude/skills` ou `~/.agents/skills` (`npx skills add addyosmani/agent-skills -g`).

**Contexto por US.** Cada etapa é um processo novo do CLI (sem `--resume`): nenhuma conversa passa de
uma US para outra. O que atravessa US é só o que o orquestrador injeta: discovery, grafo e a lista das
US já entregues ou em paralelo.

Em **Branch base** o painel lista o repositório e os repositórios git aninhados nele (ex.: api e
front clonados dentro da pasta), cada um com suas branches locais e remotas. A branch escolhida
para cada um é a de desenvolvimento: toda execução nova troca para ela antes de criar as branches
`faztudo/`. Repositório sem escolha fica na branch atual. O trabalho de cada US só entra na branch
base por **merge ao fim da onda** (ver abaixo); o FazTudo nunca commita direto nela.

## Fluxo dos agentes

0. **US em paralelo** — com mais de uma US no documento, uma **análise de dependências** (somente
   leitura, logo após o discovery) decide quais US dependem de quais e agrupa as independentes em
   **ondas**. As US da mesma onda rodam ao mesmo tempo, cada uma na sua branch e worktree git; cada
   agente sabe quais US correm ao lado e não invade o escopo delas. No fim da onda, uma branch
   `faztudo/integra-…` recebe o merge de todas e **essa branch é mergeada na branch base (ex.:
   `develop`) antes da onda seguinte começar**: a próxima onda parte da base já com a anterior. Em
   sequência (uma US por onda, ou sem worktrees) o merge na base acontece ao fim de cada US, e o
   repositório termina na base. Se o merge na base falhar (a base mudou e conflita), a execução para
   com a branch preservada para você resolver. Repositório que começa em uma branch `faztudo/`
   (retomada) sem base configurada não é mergeado. Conflito entre US vai
   para um agente de **integração**, que resolve os marcadores e roda os testes; se não resolver, o
   merge é desfeito e a US fica *Com pendências* na própria branch. Falha numa US cancela as demais da
   onda (o trabalho interrompido fica salvo em commit na branch); **Refazer etapa** retoma a US na
   própria branch, sem refazer as que já foram entregues. Repositórios aninhados (ex.: api e front
   clonados dentro do repositório pai) ganham worktree e branch próprias em cada US e são integrados
   junto. Sem git as US rodam em sequência; sem resposta legível da análise, também.
   Cada US paralela recebe sua faixa de portas (4400 + 20 por US) para servidores locais, e os agentes
   só encerram processos pelo PID ou pela porta, nunca por nome (`pkill`/`killall`). O prompt vai pelo
   stdin do CLI, fora da linha de comando, para que um `pkill -f` não derrube o agente de outra US.
1. **Discovery** (uma vez por repositório) → **Passo 0** (verifica o que o repositório já implementa,
   critério a critério, e decide: `reuse` encerra a US sem implementar; `block` pula a US apontando a
   dependência ou decisão que falta; `extend`/`refactor`/`implement` seguem, implementando só o delta)
   → **Especificação** → **Arquiteto**. Essas etapas, a segurança e o code review
   rodam **somente leitura** (`--permission-mode plan` / `--sandbox read-only`): planejadores e
   revisores não conseguem alterar arquivos, independentemente do prompt.
2. O arquiteto devolve um plano em JSON com os agentes necessários entre `frontend`, `backend`,
   `security`, `e2e` e `docs`; os demais aparecem como *Não necessária* e não são chamados. O **code review**
   sempre roda. Se o plano não vier, todos os agentes rodam (em simulação, a escolha é feita por
   palavras-chave da US). O plano traz também o **padrão de projeto**: o do projeto (identificado no
   discovery) ou, sem um, o mais adequado (componentes no frontend, Clean Architecture no backend).
   Implementadores e revisores seguem esse padrão e regras de clean code (nomes que revelam intenção,
   funções de uma responsabilidade com até ~20 linhas e 3 argumentos, guard clauses).
3. **Frentes paralelas** — o arquiteto mapeia as atividades em `streams` (agente, arquivos,
   dependências). Frentes sem dependência entre si e com arquivos disjuntos formam uma onda e rodam
   em paralelo, cada uma em uma worktree git própria; no fim da onda são integradas por merge na
   branch da US. Frente que conflita com outra é refeita sobre o código integrado. Sem git as
   frentes rodam em sequência. Sem nada a paralelizar, o fluxo segue
   com frontend e backend como etapas comuns.
4. **Segurança**, **code review** e **E2E** são portões de qualidade: devolvem um veredito JSON
   (`approved` / `changes_requested`) com os problemas e o agente responsável por cada um. Havendo
   falhas, o frontend e/ou backend recebem um prompt com exatamente o que corrigir e o mesmo portão
   roda de novo para verificar. Após 2 rodadas sem aprovação, a etapa fica *Com pendências* (listadas) e o pipeline segue para as
   próximas etapas e US, sem parar a execução.
5. O **code review** roda com revisores em paralelo, cada um com um foco: aderência à especificação,
   simplicidade e legibilidade, testes e — quando o frontend participa — interface e acessibilidade.
   Os problemas são unificados em um único veredito.
6. **Documentação do fluxo** (`docs`, quando o arquiteto julga necessária) roda por último:
   guia de uso para o cliente final e/ou fluxo, contratos e pontos de extensão para quem fará
   manutenção, no lugar onde o projeto já documenta (README, `docs/`, ADR) ou em `docs/<us>.md`.
7. Instruções do projeto (`AGENTS.md`, `CLAUDE.md`, `.github/copilot-instructions.md`, `.cursorrules`)
   são repassadas a todos os agentes, seja qual for o CLI.

## Portões de qualidade

Configurados por repositório em **Portões de qualidade** (card do repositório) e salvos com o resto
da configuração.

- **Verificação executável** — depois da implementação (e após cada rodada de correção pedida por
  segurança, review ou E2E, antes de chamar o revisor de novo; sem etapa extra), o próprio
  orquestrador roda os comandos do projeto (detectados em `package.json`, `go.mod`, `pyproject.toml`…
  ou definidos manualmente, com `CI=1` e tempo limite), checa a sintaxe dos JS alterados e dos
  `<script>` inline de HTML (`node --check`), `gofmt` e linhas longas/código minificado. Cada falha
  volta ao implementador responsável com a saída do comando; até 3 rodadas; depois disso a US segue *Com pendências*.
- **Resiliência** — cada chamada de agente tem tempo limite (60 min); o processo e os filhos (ex.: um
  `ng serve` esquecido) são encerrados juntos. Chamada que falha ou expira é refeita até 3 vezes, com
  espera crescente e aviso para o agente retomar de onde parou.
- **Rastreabilidade** — IDs de casos de teste das tabelas de testes do documento (ex.: `UT-CART-01`,
  `IT-08`) e os listados pela especificação precisam aparecer nos arquivos de teste.
- **Arquivos protegidos** — `US*.md`, `EDD*.md` e o `.md` aberto; se um agente alterar um deles, o
  conteúdo é restaurado e a etapa falha.
- **Git** — uma branch `faztudo/<us>-<data>` por US e um commit por etapa, com diff no painel.
  Repositórios sem git são inicializados; alterações pendentes em arquivos versionados impedem a execução.
- **Best-of-N** — com 2 ou 3 candidatos, cada implementador gera alternativas em paralelo, cada uma
  com uma estratégia (mudança mínima, robustez, simplicidade) em um `git worktree` próprio. Todas
  passam pelas verificações; vence a com menos problemas e, em empate, um seletor compara os diffs.
  Multiplica o consumo de tokens.

## Avaliação (benchmark)

`faztudo -eval suite.json` roda cada configuração (braço) em cada tarefa a partir de uma cópia limpa
do repositório-modelo e gera `results.json` e `report.md`. Veja [evals/README.md](evals/README.md).

## User Stories em Markdown

Use **Abrir .md** (ou cole o texto) para carregar as User Stories. Cada título de seção no formato
`US-101`, `HU 3`, `User Story …` ou `História …` vira uma US; o texto antes da primeira (épico,
regras gerais) é enviado como contexto comum a todas. Títulos dentro de blocos de código são ignorados.

Quando o documento tem mais de uma US, o painel pergunta como desenvolver:

- **Desenvolvimento contínuo** — as US selecionadas rodam em sequência, na ordem do documento.
  O discovery é feito uma vez e cada US recebe a lista das anteriores, já implementadas no repositório.
  Uma falha interrompe a sequência.
- **Apenas uma US** — desenvolve somente a US escolhida.

## Provedores e modelos

No formulário de execução escolha o **provedor padrão** (Claude Code ou Codex CLI) e se o
**failover** para o outro provedor fica ativo. Em **Modelos** defina, por provedor, o modelo
*heavy* (arquitetura, segurança, review), o *standard* (demais etapas) e o nível de esforço —
a lista do **Codex** vem do catálogo do próprio CLI instalado (`~/.codex/models_cache.json`, ou
`$CODEX_HOME`), relido a cada minuto, e só oferece os níveis de esforço que os modelos escolhidos
suportam; o padrão inicial segue o `model`/`model_reasoning_effort` do `~/.codex/config.toml`.
A lista do Claude é interna. A opção "Outro…" aceita qualquer ID (com aviso se não estiver no catálogo).
O discovery roda em modo somente leitura (`--sandbox read-only` no Codex, `--permission-mode plan` no Claude). Sem failover, um limite
de uso faz a execução aguardar a liberação do provedor padrão.

### Persistência

Provedor padrão, failover, modelos/esforço e o repositório são salvos a cada alteração em
`~/.config/faztudo/config.json` (ou `$XDG_CONFIG_HOME/faztudo/config.json`), com escrita atômica e
permissão `0600`, e recarregados ao iniciar. Flags e variáveis de ambiente explícitas (`-provider`,
`-repo`) têm prioridade sobre o arquivo. Entradas inválidas (repositório removido, modelo com esforço
não suportado) são ignoradas com aviso no log, sem descartar o resto. O discovery é salvo ao lado,
em `discovery/`; **Refazer** apaga o do repositório.

**Última execução.** A execução de cada repositório é salva em `runs/` (ao lado da configuração)
enquanto roda: etapas e saídas (plano do arquiteto, especificação, relatórios), branches, ondas,
logs e as US com o contexto do documento. Ao abrir o app ou trocar de repositório, a última execução
volta à tela. Se ela parou (falha, **Parar**, app fechado ou travado no meio), o botão
**Continuar de onde parou** retoma na etapa marcada *Parou aqui*, na branch da US, reaproveitando as
etapas concluídas (`POST /api/pipeline/resume`). Só a janela que executou grava o arquivo, para uma
janela ociosa no mesmo repositório não sobrescrever uma execução mais nova.

## Requisitos

- Go 1.22+ (app desktop: CLI do Wails; Linux: libgtk-3-dev e libwebkit2gtk-4.1-dev)
- Opcional, para execução real: [`claude`](https://docs.claude.com/claude-code) e/ou [`codex`](https://github.com/openai/codex) no `PATH`

Sem os CLIs, a aplicação funciona em **modo simulação** (padrão), que percorre todo o fluxo sem consumir tokens.

## App desktop

FazTudo roda como aplicativo nativo ([Wails](https://wails.io)): janela própria com o
WebView do sistema (WKWebView no macOS, WebView2 no Windows, WebKitGTK no Linux), sem
navegador e sem porta TCP aberta. A API roda no mesmo processo.

| Sistema | Instalador | Como gerar |
|---------|------------|------------|
| macOS 13+ (Intel e Apple Silicon) | `FazTudo-<versão>-macos-universal.dmg` | `make dmg` (local) ou CI |
| Windows 10/11 x64 | `FazTudo-<versão>-windows-amd64-setup.exe` | CI (`make desktop-windows` exige `makensis`) |
| Linux x64 | `.deb`, `.rpm`, `.tar.gz` | CI (`make desktop-linux` + `scripts/package-linux.sh` no Linux) |

Publicar versão: `git tag v0.2.0 && git push origin v0.2.0`. O workflow
[`desktop-release`](../.github/workflows/desktop-release.yml) compila nos três sistemas e anexa
os instaladores à Release. Rodado manualmente (*Run workflow*), deixa os arquivos em *Artifacts*.

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0   # uma vez
make desktop-dev  # janela com recarga dos assets
make install-mac  # compila e substitui /Applications/FazTudo.app (sem cópia em build/bin)
make dmg          # build/bin/FazTudo.app + dist/*.dmg
../start.sh       # instala e abre o app (macOS); ../start.sh --web para o modo servidor
```

Notas:

- **Várias janelas:** *Arquivo › Nova janela* (⌘N / Ctrl+N) abre outra instância do app. Cada janela
  tem sua execução e pode apontar para outro repositório; no mesmo repositório só uma janela executa
  por vez (trava por repositório entre processos, liberada ao fim da execução ou se o app fechar).
  Configuração e discovery salvos são compartilhados entre as janelas.
- Ao abrir pelo Finder/Dock/menu, o app carrega o `PATH` do shell de login, para achar
  `claude`, `codex`, `node` e `npx` instalados via Homebrew, nvm ou `~/.local/bin`.
- Sem assinatura Apple Developer, o macOS bloqueia o primeiro uso: clique com o botão direito
  em FazTudo.app → *Abrir*, ou `xattr -dr com.apple.quarantine /Applications/FazTudo.app`.
  No Windows, o SmartScreen pede *Mais informações → Executar assim mesmo*.
- Linux depende de `libwebkit2gtk-4.1-0` (Ubuntu 22.04+/Debian 12+), instalada pelo `.deb`/`.rpm`.

## Modo servidor (web/Docker)

```bash
make run          # compila em bin/ e inicia em http://127.0.0.1:8081
make dev          # go run com logs em nível debug
../start.sh --web # atalho a partir da raiz do repositório
```

| Flag | Variável | Padrão | Descrição |
|------|----------|--------|-----------|
| `-addr` | `ADDR` / `PORT` | `127.0.0.1:8081` | Endereço HTTP |
| `-repo` | `REPO_PATH` | — | Repositório alvo inicial |
| `-provider` | `DEFAULT_PROVIDER` | `claude` | Provedor padrão (`claude` ou `codex`) |
| `-config` | `FAZTUDO_CONFIG` | `~/.config/faztudo/config.json` | Arquivo de configuração (`""` desativa) |
| `-dry-run` | `DRY_RUN` | `true` | Modo inicial (simulação) |
| `-log-level` | `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `-sim-delay` | — | `1.4s` | Duração de cada etapa simulada |
| `-eval` | — | — | Roda uma suíte de avaliação e sai |
| `-version` | — | — | Exibe a versão |

> **Segurança.** Em execução real os CLIs rodam com permissões automáticas
> (`--dangerously-skip-permissions` / `--sandbox workspace-write`), e os comandos de verificação
> configurados rodam no shell. Por isso o servidor escuta apenas em
> `127.0.0.1` por padrão e rejeita requisições de escrita vindas de outras origens.
> Não exponha a porta em redes não confiáveis.

## Desenvolvimento

```bash
make test     # testes com -race
make lint     # gofmt + go vet
make cover    # cobertura
make docker   # imagem distroless (modo simulação)
```

## Estrutura

```text
main.go                  # flags, wiring e graceful shutdown
internal/provider/       # definição dos CLIs, parser de quota e router com failover
internal/settings/       # persistência da configuração em JSON
internal/stories/        # parser de Markdown em User Stories
internal/workspace/      # repositório alvo, stacks, discovery, política de qualidade e instruções
internal/quality/        # comandos, sintaxe, legibilidade, rastreabilidade e arquivos protegidos
internal/vcs/            # git: branches, commits por etapa e worktrees
internal/pipeline/       # etapas, prompts, revisores paralelos, best-of-N e orquestrador
internal/eval/           # benchmark de configurações
internal/server/         # API JSON, SSE e middlewares (segurança, logs, recover)
web/static/              # dashboard (HTML/CSS/JS sem dependências externas, embutido no binário)
```

## API

| Método | Rota | Descrição |
|--------|------|-----------|
| `GET` | `/api/status` | Snapshot completo (execução, etapas, eventos, provedores) |
| `GET` | `/api/events` | Stream SSE com o mesmo snapshot a cada mudança |
| `GET` / `PUT` | `/api/workspace` | Repositório alvo (`{"path": "~/projetos/app"}`) e discovery em cache |
| `DELETE` | `/api/workspace/discovery` | Descarta o discovery salvo do repositório |
| `PUT` | `/api/workspace/policy` | Portões de qualidade do repositório (`auto_checks`, `checks`, `protected`, `git`, `max_line_length`, `check_timeout_s`, `candidates`, `base_branches`) |
| `GET` | `/api/workspace/repos` | Repositório e aninhados com a branch atual e as disponíveis para branch base |
| `GET` | `/api/git/commits/{hash}` | Diff de um commit feito pelo pipeline |
| `POST` | `/api/stories/parse` | `{"content": "<markdown>"}` → `{"preamble": "...", "stories": [{"id", "title", "content"}]}` |
| `POST` | `/api/pipeline/start` | `{"stories": [...], "preamble": "...", "source": "markdown", "dry_run": true, "rediscover": false, "document_name": "US.md"}`; ou `{"content": "..."}` com uma única US |
| `POST` | `/api/pipeline/stop` | Cancela a execução em andamento |
| `POST` | `/api/pipeline/retry` | Refaz a etapa com falha (`{"story": 0, "step": "review"}`; `story` -1 para discovery/análise) |
| `POST` | `/api/pipeline/resume` | Continua a última execução parada (falha, cancelada ou app fechado) de onde parou |
| `GET` / `PUT` | `/api/config` | `{"default_provider": "codex", "failover": true, "providers": {"claude": {"heavy_model", "std_model", "effort"}}}` (atualização parcial) |
| `POST` | `/api/providers/{id}/exhaust` | Simula limite de uso (`{"seconds": 45}`) |
| `POST` | `/api/providers/{id}/restore` | Restaura a cota de um provedor |
| `POST` | `/api/providers/restore` | Restaura todas as cotas |
| `GET` | `/healthz` | Health check |

## Próximos passos

Veja o [blueprint](../blueprint_e_guia_de_constru_o_orquestrador_de_agentes_llm.md): worktrees por subagente,
execução real do Playwright, conectores Jira/Trello e persistência de checkpoints.
