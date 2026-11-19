# Quickstart: Saúde do código

## Pré-requisitos

- `cd llm-orchestrator && make build`
- Repositório de teste com git, política com `git: true` e comandos de lint/teste configurados.
- Opcional: `graphify` instalado (indicador de ciclos).

## Sugestão de áreas (US2)

```bash
./bin/faztudo health areas --repo /caminho/repo
./bin/faztudo health areas --repo /caminho/repo --json
```

Esperado: até 3 áreas com motivos, em menos de 30 s, sem nenhuma chamada de CLI de IA. Sem graphify: aviso
"grafo indisponível" e pontuação sem ciclos.

## Limpeza simulada (US1, US3)

```bash
./bin/faztudo health run --repo /caminho/repo --area internal/pagamentos --comments
```

Esperado: branch `faztudo/saude-internal-pagamentos-…` criada, etapas linha de base → limpeza → verificação,
veredito aprovado (simulação), repositório de volta à branch de base.

## Limpeza real

```bash
./bin/faztudo health run --repo /caminho/repo --area internal/pagamentos --real
```

Verificar: `git log <branch>` com um commit `faztudo(saude)`; `git diff <base>...<branch> --stat` só dentro da
área; registro de decisões no resultado; nenhuma falha nova em relação à linha de base.

## Reprovação

Em um repositório de teste, ajustar o executor falso para apagar um teste `UT-…` ou mexer fora da área:
veredito `rejected` com o motivo, branch mantida, base intacta.

## Interface

Abrir o painel → seção "Saúde do código" → "Sugerir áreas" → escolher uma → marcar opções → "Iniciar limpeza".
Acompanhar como uma execução de US.

## Gates

```bash
cd llm-orchestrator && make lint && make test
```
