# Distribuição e plataformas

## Suporte

| Plataforma | Situação | Por quê |
|---|---|---|
| macOS 13+ (Intel e Apple Silicon) | suportado; CI e release | travas (flock), grupo de processos e `/bin/sh` validados |
| Linux x64 (Ubuntu 22.04+, Debian 12+) | suportado; CI e release | idem |
| Windows | **não suportado** | os comandos do projeto rodam via `/bin/sh`, as travas de repositório, de stack e de recurso não existem fora de Unix e o cancelamento por grupo de processos não foi validado |

Uma plataforma só entra no CI e no release depois de validar o runner de comandos, as travas, o
cancelamento e o empacotamento nela. Em Windows, use o modo servidor dentro do WSL.

## Versões

- Go: o do `go.mod` (`go 1.25`); CI e release usam `go-version-file`.
- Wails: fixado em `WAILS_VERSION` no workflow de release.
- Skills incorporadas: fixadas por commit (`bundle/AGENT_SKILLS_COMMIT`) e por hash
  (`internal/skills/testdata/playbooks.sha256`).
- Dependências Go: `go.sum` versionado; atualize com `go get` em PR próprio.

## Release

Uma tag `vX.Y.Z` dispara o workflow de release, que roda primeiro o mesmo CI dos PRs e depois gera o
`.dmg` (macOS) e `.deb`/`.rpm`/`.tar.gz` (Linux).
