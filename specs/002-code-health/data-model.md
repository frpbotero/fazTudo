# Modelo de dados: Saúde do código

## CleanupOptions (entrada)

| Campo | Tipo | Regra |
|---|---|---|
| `area` | texto | caminho relativo de diretório existente no repositório; `.` só explícito; sem `..` nem absoluto |
| `comments` | bool | remover comentários que repetem o código |
| `tests` | bool | remover/reescrever testes que não pegam bug |
| `rounds` | inteiro | 1–5, padrão 3 |
| `dry_run` | bool | simulação (padrão no console e na interface, como nas US) |

## AreaScore (sugestão)

| Campo | Tipo | Significado |
|---|---|---|
| `path` | texto | área candidata |
| `score` | número 0–4 | soma dos indicadores normalizados |
| `churn` | inteiro | mudanças nos últimos 500 commits |
| `co_change` | inteiro | pares que mudam juntos (entre áreas contam em dobro) |
| `cycles` | inteiro | ciclos de dependência envolvendo a área; ausente sem grafo |
| `special_cases` | inteiro | `sleep`, `retry`, teste pulado, exceção engolida |
| `reasons` | lista de texto | indicadores que mais pesaram, em português |

`AreaSuggestion` = `{areas: AreaScore[≤3], commits: n, graph: bool, warnings: texto[]}`.

## Run (estende o existente)

| Campo novo | Tipo | Regra |
|---|---|---|
| `kind` | `"stories"` \| `"cleanup"` | vazio = `stories` (compatível com journals antigos) |
| `cleanup` | CleanupInfo | presente só quando `kind = cleanup` |

`Stories` tem uma `StoryRun` com título `Saúde do código: <área>`, branch da limpeza e etapas `baseline`,
`cleanup`, `cleanup-verify`.

## CleanupInfo

| Campo | Tipo | Significado |
|---|---|---|
| `area`, `comments`, `tests`, `rounds` | — | opções usadas |
| `base_branch` | texto | branch de onde saiu |
| `baseline` | CheckSnapshot[] | resultado de cada comando antes |
| `final` | CheckSnapshot[] | resultado de cada comando depois |
| `verdict` | `approved` \| `rejected` \| vazio | vazio enquanto roda ou se cancelada |
| `reasons` | texto[] | por que reprovou (falha nova, arquivo fora da área, ID de teste removido, entrada alterada) |
| `changed` | texto[] | arquivos alterados |
| `decision_log` | texto | registro de decisões (até 64 KB) |

## CheckSnapshot

| Campo | Tipo |
|---|---|
| `name` | texto (nome do comando da política) |
| `passed` | bool |
| `timed_out` | bool |
| `fingerprints` | texto[] |

## Transições

```text
StartCleanup ─► baseline ─► cleanup (agente) ─► cleanup-verify ─► approved | rejected
      │             │              │                    │
      └── recusa: repositório ocupado, sem git, área inválida, árvore suja
                    └──── Stop em qualquer etapa: cancelada, branch mantida, volta à base
```

Regras do veredito (`rejected` se qualquer uma valer): fingerprint novo; comando que passava falha ou estoura
o tempo; arquivo alterado fora da área; ID de teste de critério de aceite sumiu; guard de entrada disparou.
