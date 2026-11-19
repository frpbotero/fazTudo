# Contrato: console

```text
faztudo health areas [--repo DIR] [--json]
faztudo health run --area CAMINHO [--repo DIR] [--comments] [--tests] [--rounds N] [--real]
```

- `health areas`: imprime até 3 áreas com pontuação e motivos; `--json` imprime `AreaSuggestion`
  ([data-model](../data-model.md)). Avisos (sem grafo, histórico curto) vão para stderr. Não chama modelo.
- `health run`: inicia a limpeza e acompanha em primeiro plano como `run` (eventos, Ctrl+C para, `status`/`stop`
  de outro terminal funcionam). Simulação por padrão; `--real` executa de verdade.
- Saída final: veredito, branch, arquivos alterados, motivos da reprovação e caminho do registro de decisões.
- Códigos de saída: `0` aprovada; `1` reprovada ou erro; `2` uso inválido (área ausente, `--rounds` fora de 1–5).
- `faztudo help` e a TUI listam os dois comandos; a TUI os executa pela linha de comandos contextual.
