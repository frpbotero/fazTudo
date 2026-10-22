#!/usr/bin/env bash
# Empacota build/bin/FazTudo.app em dist/FazTudo-<versão>-macos-universal.dmg.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
VERSION="${1:-dev}"
APP="build/bin/FazTudo.app"
OUT="dist/FazTudo-${VERSION}-macos-universal.dmg"

[[ -d "$APP" ]] || { echo "erro: $APP não existe; rode 'make desktop-mac' antes" >&2; exit 1; }

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
cp -R "$APP" "$STAGE/"
ln -s /Applications "$STAGE/Applications"

mkdir -p dist
rm -f "$OUT"
hdiutil create -volname "FazTudo" -srcfolder "$STAGE" -fs HFS+ -format UDZO -ov "$OUT" >/dev/null
echo "$OUT"
