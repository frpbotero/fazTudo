# FazTudo — Orquestrador de agentes

Servidor Go com dashboard web que conduz uma User Story por um pipeline de agentes LLM
(especificação EDD/TDD → arquitetura → frontend → backend → segurança → code review → E2E),
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

1. **Discovery** (uma vez por sessão, somente leitura) → **Especificação** → **Arquiteto**.
2. O arquiteto devolve um plano em JSON com os agentes necessários entre `frontend`, `backend`,
   `security` e `e2e`; os demais aparecem como *Não necessária* e não são chamados. O **code review**
   sempre roda. Se o plano não vier, todos os agentes rodam (em simulação, a escolha é feita por
   palavras-chave da US).
3. **Segurança**, **code review** e **E2E** são portões de qualidade: devolvem um veredito JSON
   (`approved` / `changes_requested`) com os problemas e o agente responsável por cada um. Havendo
   falhas, o frontend e/ou backend recebem um prompt com exatamente o que corrigir e o mesmo portão
   roda de novo para verificar. Após 2 rodadas sem aprovação, a US falha com as pendências listadas.

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

- Go 1.22+
- Opcional, para execução real: [`claude`](https://docs.claude.com/claude-code) e/ou [`codex`](https://github.com/openai/codex) no `PATH`

Sem os CLIs, a aplicação funciona em **modo simulação** (padrão), que percorre todo o fluxo sem consumir tokens.

## Uso

```bash
make run          # compila em bin/ e inicia em http://127.0.0.1:8081
make dev          # go run com logs em nível debug
../start.sh       # atalho a partir da raiz do repositório
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
| `-version` | — | — | Exibe a versão |

> **Segurança.** Em execução real os CLIs rodam com permissões automáticas
> (`--dangerously-skip-permissions` / `--full-auto`). Por isso o servidor escuta apenas em
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
internal/workspace/      # repositório alvo, varredura de stacks e cache do discovery
internal/pipeline/       # etapas, prompts e orquestrador (estado observável)
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
| `POST` | `/api/stories/parse` | `{"content": "<markdown>"}` → `{"preamble": "...", "stories": [{"id", "title", "content"}]}` |
| `POST` | `/api/pipeline/start` | `{"stories": [...], "preamble": "...", "source": "markdown", "dry_run": true, "rediscover": false}`; ou `{"content": "..."}` com uma única US |
| `POST` | `/api/pipeline/stop` | Cancela a execução em andamento |
| `GET` / `PUT` | `/api/config` | `{"default_provider": "codex", "failover": true, "providers": {"claude": {"heavy_model", "std_model", "effort"}}}` (atualização parcial) |
| `POST` | `/api/providers/{id}/exhaust` | Simula limite de uso (`{"seconds": 45}`) |
| `POST` | `/api/providers/{id}/restore` | Restaura a cota de um provedor |
| `POST` | `/api/providers/restore` | Restaura todas as cotas |
| `GET` | `/healthz` | Health check |

## Próximos passos

Veja o [blueprint](../blueprint_e_guia_de_constru_o_orquestrador_de_agentes_llm.md): worktrees por subagente,
execução real do Playwright, conectores Jira/Trello e persistência de checkpoints.
