#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ $# -ne 2 ]]; then
  echo "usage: scripts/import-mattpocock-skill.sh <engineering|productivity> <skill-name>" >&2
  exit 2
fi

group="$1"
name="$2"
pin=4ae2317ddb22b046e26455e4f9c9b3aaaf62212d
src=/tmp/mattpocock-skills
dest=".cursor/skills/$name"
overlay=.cursor/skills/_overlay/setup-constraints.md

case "$group" in
  engineering|productivity) ;;
  *) echo "group must be engineering or productivity" >&2; exit 2 ;;
esac

if [[ -d "$dest" ]] && ! grep -qx "$name" scripts/mattpocock-skill-manifest.txt; then
  echo "refusing to overwrite existing skill: $name" >&2
  exit 1
fi

if [[ ! -d "$src/.git" ]] || [[ "$(git -C "$src" rev-parse HEAD)" != "$pin" ]]; then
  rm -rf "$src"
  git clone --depth 1 https://github.com/mattpocock/skills.git "$src"
  git -C "$src" fetch --depth 1 origin "$pin"
  git -C "$src" checkout --detach "$pin"
fi

upstream="$src/skills/$group/$name"
if [[ ! -f "$upstream/SKILL.md" ]]; then
  echo "missing upstream skill: $upstream/SKILL.md" >&2
  exit 1
fi

rm -rf "$dest"
mkdir -p "$dest"
while IFS= read -r rel; do
  mkdir -p "$dest/$(dirname "$rel")"
  cp "$upstream/$rel" "$dest/$rel"
done < <(cd "$upstream" && find . -type f ! -path './agents/*' | sed 's|^\./||')

python3 - "$dest/SKILL.md" "$overlay" <<'PY'
import pathlib, sys
skill = pathlib.Path(sys.argv[1])
overlay = pathlib.Path(sys.argv[2]).read_text()
text = skill.read_text()
if "## Setup constraints" in text:
    sys.exit(0)
parts = text.split("---", 2)
if len(parts) < 3:
    raise SystemExit(f"no frontmatter: {skill}")
body = parts[2]
if not body.startswith("\n"):
    body = "\n" + body
skill.write_text("---" + parts[1] + "---\n\n" + overlay.strip() + "\n" + body)
PY
