# Verificação contínua

O workflow [`ci`](../.github/workflows/ci.yml) roda em todo pull request e em push para `main` e
`develop`, no Ubuntu 24.04 e no macOS (as plataformas das releases):

| Passo | Comando (em `llm-orchestrator/`) |
|---|---|
| Sem CLIs de agente no PATH | falha se `claude`, `codex` ou `gemini` estiverem instalados no runner |
| Formatação e análise estática | `make lint` (gofmt + go vet) |
| Testes | `make test` (go test -race ./...) |
| Build | `make build` |

Os testes usam CLIs falsos, providers simulados e repositórios git temporários. Não dependem de
nenhum CLI real nem de quota de LLM: o primeiro passo garante isso. Os casos de segurança de aprovação
(veredito inválido, validação, promoção, regressão do conjunto integrado, base alterada) e de retomada
fazem parte da suíte normal, então rodam em todo PR.

A release ([`desktop-release`](../.github/workflows/desktop-release.yml), por tag `v*` ou manual)
chama o mesmo workflow sobre o commit da tag antes de gerar os instaladores. Nada é empacotado sem
passar pela mesma verificação de um PR.

## Proteção das branches

Configure em *Settings → Branches* (ou *Rulesets*) para `main` e `develop`:

- Exigir pull request antes do merge.
- Exigir que os checks passem, com a branch atualizada: `verify (ubuntu-24.04)` e
  `verify (macos-latest)`.
- Bloquear push direto e force push.

Pelo `gh`, com permissão de administrador (repita para `develop`):

```sh
gh api -X PUT repos/<dono>/<repo>/branches/main/protection --input - <<'JSON'
{
  "required_status_checks": {"strict": true, "contexts": ["verify (ubuntu-24.04)", "verify (macos-latest)"]},
  "enforce_admins": false,
  "required_pull_request_reviews": {"required_approving_review_count": 0},
  "restrictions": null
}
JSON
```

Os nomes dos checks só ficam disponíveis para seleção depois que o workflow rodar uma vez.
