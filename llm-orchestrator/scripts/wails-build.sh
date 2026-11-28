#!/usr/bin/env bash
# Roda "wails build" com a versão do app em wails.json, para que o Info.plist
# do macOS e o instalador NSIS levem a versão da release em vez do valor fixo
# do arquivo. O wails.json original volta ao fim, mesmo em erro.
# Uso: scripts/wails-build.sh <versão> <wails> [argumentos do wails build...]
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
VERSION="${1:?informe a versão}"
WAILS="${2:?informe o comando wails}"
shift 2

[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]] || {
	echo "erro: versão inválida: $VERSION (use X.Y.Z)" >&2
	exit 1
}

cp wails.json wails.json.orig
trap 'mv -f wails.json.orig wails.json' EXIT
sed -E "s/(\"productVersion\"[[:space:]]*:[[:space:]]*\")[^\"]*\"/\1${VERSION}\"/" wails.json.orig >wails.json
grep -q "\"productVersion\": *\"${VERSION}\"" wails.json || {
	echo "erro: productVersion não encontrado em wails.json" >&2
	exit 1
}

"$WAILS" build "$@"
