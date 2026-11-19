#!/usr/bin/env bash
# Atualiza as skills de addyosmani/agent-skills embutidas em internal/skills/bundle.
# Uso: scripts/update-skills.sh [commit]   (sem argumento: o commit fixado abaixo)
# As skills próprias do FazTudo (ex.: structural-health) não são tocadas.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
COMMIT="${1:-8c99fa36d90068c82d547f7c12a9d67a3e95f4ee}"
BUNDLE="internal/skills/bundle"
# Mesma lista de internal/pipeline/stages.go (TestStageSkillsAreBundled confere).
SKILLS=(
  spec-driven-development planning-and-task-breakdown api-and-interface-design
  incremental-implementation test-driven-development frontend-ui-engineering
  security-and-hardening code-review-and-quality code-simplification
  documentation-and-adrs debugging-and-error-recovery
)

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
curl -fsSL "https://codeload.github.com/addyosmani/agent-skills/tar.gz/${COMMIT}" | tar xz -C "$TMP"
SRC="$(ls -d "$TMP"/agent-skills-*)"

for s in "${SKILLS[@]}"; do
  [[ -f "$SRC/skills/$s/SKILL.md" ]] || { echo "erro: skill $s não existe em $COMMIT" >&2; exit 1; }
  rm -rf "${BUNDLE:?}/skills/$s"
  mkdir -p "$BUNDLE/skills"
  cp -R "$SRC/skills/$s" "$BUNDLE/skills/$s"
done

# Checklists compartilhados que as skills citam como ../../references/<arquivo>.
rm -rf "${BUNDLE:?}/references"
mkdir -p "$BUNDLE/references"
for s in "${SKILLS[@]}"; do
  grep -ohE '\.\./\.\./references/[A-Za-z0-9_-]+\.md' "$BUNDLE/skills/$s/SKILL.md" || true
done | sort -u | while read -r ref; do
  cp "$SRC/references/$(basename "$ref")" "$BUNDLE/references/"
done

cp "$SRC/LICENSE" "$BUNDLE/LICENSE-agent-skills"
echo "$COMMIT" > "$BUNDLE/AGENT_SKILLS_COMMIT"
echo "Skills atualizadas para addyosmani/agent-skills@${COMMIT:0:7}."
