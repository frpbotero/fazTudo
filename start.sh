#!/usr/bin/env bash
# Compila e inicia o FazTudo. Variáveis: PORT (8081), ADDR, DRY_RUN (true), LOG_LEVEL (info).
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/llm-orchestrator"
make build
exec ./bin/faztudo "$@"
