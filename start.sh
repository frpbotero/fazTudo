#!/usr/bin/env bash
# Compila e abre o FazTudo.
#   ./start.sh        app desktop (macOS: build/bin/FazTudo.app; Linux: build/bin/FazTudo)
#   ./start.sh --web  modo servidor em http://127.0.0.1:8081 (PORT, ADDR, DRY_RUN, LOG_LEVEL)
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/llm-orchestrator"

if [[ "${1:-}" == "--web" ]]; then
  shift
  make build
  exec ./bin/faztudo "$@"
fi

export PATH="$PATH:$(go env GOPATH)/bin"
command -v wails >/dev/null || go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0

case "$(uname -s)" in
  Darwin)
    make desktop-mac
    open build/bin/FazTudo.app
    ;;
  Linux)
    make desktop-linux
    exec ./build/bin/FazTudo
    ;;
  *)
    echo "Use o instalador do Windows ou ./start.sh --web" >&2
    exit 1
    ;;
esac
