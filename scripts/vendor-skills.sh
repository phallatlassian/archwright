#!/usr/bin/env bash
# Re-vendor the mattpocock skills snapshot (ADR 0005). This is a deliberate,
# reviewed change, never run by the Works: clone upstream yourself, check out
# the commit to pin, then run
#
#   scripts/vendor-skills.sh <path-to-upstream-clone>
#
# It replaces internal/skills/vendor/upstream with the clone's engineering/ and
# productivity/ skills (flattened by skill name) and rewrites PROVENANCE.json.
# Forks under internal/skills/vendor/forks are left alone; review their drift
# against the recorded upstream hashes by hand.
set -euo pipefail

src=${1:?usage: vendor-skills.sh <upstream-clone>}
root=$(cd "$(dirname "$0")/.." && pwd)
dest=$root/internal/skills/vendor
url=$(git -C "$src" remote get-url origin)
sha=$(git -C "$src" rev-parse HEAD)
date=$(git -C "$src" show -s --format=%cI HEAD)
[ -z "$(git -C "$src" status --porcelain)" ] || { echo "upstream clone is dirty" >&2; exit 1; }

rm -rf "$dest/upstream"
mkdir -p "$dest/upstream"
for cat in engineering productivity; do
  for d in "$src"/skills/$cat/*/; do
    name=$(basename "$d")
    [ -e "$dest/upstream/$name" ] && { echo "duplicate skill name $name" >&2; exit 1; }
    cp -R "$d" "$dest/upstream/$name"
  done
done

files=$(cd "$dest/upstream" && find . -type f | sort | sed 's|^\./||' | while read -r f; do
  printf '%s  %s\n' "$(openssl dgst -sha256 -r "$f" | cut -d' ' -f1)" "$f"
done | jq -R 'split("  ") | {(.[1]): .[0]}' | jq -s add)


[ -f "$dest/PROVENANCE.json" ] && prev_forks=$(jq '.forks' "$dest/PROVENANCE.json")

jq -n --arg url "$url" --arg sha "$sha" --arg date "$date" --arg at "$(date -u +%Y-%m-%d)" \
  --argjson files "$files" --argjson forks "$prev_forks" '{
  source: $url,
  commit: $sha,
  commit_date: $date,
  vendored_on: $at,
  license: "MIT (Matt Pocock)",
  paths: ["skills/engineering/*", "skills/productivity/*"],
  note: "Vendored verbatim under upstream/<skill>; never fetched at runtime (ADR 0005). Upgrade by re-running scripts/vendor-skills.sh against a reviewed commit.",
  forks: ($forks | map(. as $f | .upstream_files = ($files | with_entries(select(.key | startswith($f.name + "/")))))),
  files: $files
}' > "$dest/PROVENANCE.json"
cp "$src/LICENSE" "$dest/LICENSE.upstream"
echo "vendored $sha"
