#!/usr/bin/env bash
# Empacota build/bin/FazTudo em dist/ como .deb, .rpm e .tar.gz (requer nfpm).
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
export VERSION="${1:-0.0.0}"
export ARCH="${2:-amd64}"

[[ -x build/bin/FazTudo ]] || { echo "erro: build/bin/FazTudo não existe; rode 'make desktop-linux' antes" >&2; exit 1; }

mkdir -p dist
nfpm package --config build/linux/nfpm.yaml --packager deb --target dist/
nfpm package --config build/linux/nfpm.yaml --packager rpm --target dist/
tar -czf "dist/FazTudo-${VERSION}-linux-${ARCH}.tar.gz" -C build/bin FazTudo
ls dist
