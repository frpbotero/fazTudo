# FazTudo — Orquestrador de agentes

Servidor Go com dashboard web que conduz uma User Story por um pipeline de agentes LLM
(contexto → especificação EDD/TDD → arquitetura → frontend → backend → verificação executável →
segurança → code review → E2E → verificação final),
usando os CLIs **Claude Code** e **Codex** com failover automático quando um deles atinge o limite de uso.

## Repositório e discovery

Os agentes trabalham dentro de um **repositório local** configurado no painel (ou via `-repo`).
A primeira execução da sessão começa por um **discovery**: uma varredura dos manifestos
(`go.mod`, `package.json`, `pyproject.toml`, `pom.xml`, Dockerfile etc.) identifica as stacks por
diretório e um agente complementa com versões, comandos de build/teste, arquitetura e convenções.

O relatório fica em memória durante a sessão (enquanto o servidor estiver rodando) e é injetado
como contexto em todos os agentes. As execuções seguintes **reutilizam o discovery** sem refazê-lo.
Ele é refeito apenas ao trocar de repositório, ao clicar em **Refazer** ou ao reiniciar o servidor.

## Fluxo dos agentes

1. **Discovery** (uma vez por sessão) → **Contexto da US** (mapeia arquivos, código a reutilizar e
   testes relacionados) → **Especificação** → **Arquiteto**. Essas etapas, a segurança e o code review
   rodam **somente leitura** (`--permission-mode plan` / `--sandbox read-only`): planejadores e
   revisores não conseguem alterar arquivos, independentemente do prompt.
2. O arquiteto devolve um plano em JSON com os agentes necessários entre `frontend`, `backend`,
   `security` e `e2e`; os demais aparecem como *Não necessária* e não são chamados. O **code review**
   sempre roda. Se o plano não vier, todos os agentes rodam (em simulação, a escolha é feita por
   palavras-chave da US).
3. **Segurança**, **code review** e **E2E** são portões de qualidade: devolvem um veredito JSON
   (`approved` / `changes_requested`) com os problemas e o agente responsável por cada um. Havendo
   falhas, o frontend e/ou backend recebem um prompt com exatamente o que corrigir e o mesmo portão
   roda de novo para verificar. Após 2 rodadas sem aprovação, a US falha com as pendências listadas.
4. O **code review** roda com revisores em paralelo, cada um com um foco: aderência à especificação,
   simplicidade e legibilidade, testes e — quando o frontend participa — interface e acessibilidade.
   Os problemas são unificados em um único veredito.
5. Instruções do projeto (`AGENTS.md`, `CLAUDE.md`, `.github/copilot-instructions.md`, `.cursorrules`)
   são repassadas a todos os agentes, seja qual for o CLI.

## Portões de qualidade

Configurados por repositório em **Portões de qualidade** (card do repositório) e salvos com o resto
da configuração.

- **Verificação executável** — depois da implementação e de novo ao final (se algo mudou), o próprio
  orquestrador roda os comandos do projeto (detectados em `package.json`, `go.mod`, `pyproject.toml`…
  ou definidos manualmente, com `CI=1` e tempo limite), checa a sintaxe dos JS alterados e dos
  `<script>` inline de HTML (`node --check`), `gofmt` e linhas longas/código minificado. Cada falha
  volta ao implementador responsável com a saída do comando; até 3 rodadas.
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
não suportado) são ignoradas com aviso no log, sem descartar o resto. O discovery **não** é salvo:
continua valendo por sessão.

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
make dmg          # build/bin/FazTudo.app + dist/*.dmg
../start.sh       # compila e abre o app (macOS); ../start.sh --web para o modo servidor
```

Notas:

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
| `DELETE` | `/api/workspace/discovery` | Descarta o discovery da sessão |
| `PUT` | `/api/workspace/policy` | Portões de qualidade do repositório (`auto_checks`, `checks`, `protected`, `git`, `max_line_length`, `check_timeout_s`, `candidates`) |
| `GET` | `/api/git/commits/{hash}` | Diff de um commit feito pelo pipeline |
| `POST` | `/api/stories/parse` | `{"content": "<markdown>"}` → `{"preamble": "...", "stories": [{"id", "title", "content"}]}` |
| `POST` | `/api/pipeline/start` | `{"stories": [...], "preamble": "...", "source": "markdown", "dry_run": true, "rediscover": false, "document_name": "US.md"}`; ou `{"content": "..."}` com uma única US |
| `POST` | `/api/pipeline/stop` | Cancela a execução em andamento |
| `GET` / `PUT` | `/api/config` | `{"default_provider": "codex", "failover": true, "providers": {"claude": {"heavy_model", "std_model", "effort"}}}` (atualização parcial) |
| `POST` | `/api/providers/{id}/exhaust` | Simula limite de uso (`{"seconds": 45}`) |
| `POST` | `/api/providers/{id}/restore` | Restaura a cota de um provedor |
| `POST` | `/api/providers/restore` | Restaura todas as cotas |
| `GET` | `/healthz` | Health check |

## Próximos passos

Veja o [blueprint](../blueprint_e_guia_de_constru_o_orquestrador_de_agentes_llm.md): worktrees por subagente,
execução real do Playwright, conectores Jira/Trello e persistência de checkpoints.
