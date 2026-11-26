#!/usr/bin/env bash
# Report which forked skills have a changed upstream base (ADR 0005). Run it
# against a candidate upstream before bumping the pin:
#
#   scripts/skills-drift.sh <path-to-upstream-clone> [<commit>]
#
# For each fork in internal/skills/vendor/PROVENANCE.json it compares the
# recorded upstream base hashes with the same files at <commit> (default HEAD)
# in the clone, prints whether the base changed, and shows a unified diff of
# the base change. Exits 1 if any fork's base changed, 0 if none did.
# It never edits anything; it works the same before or after
# scripts/vendor-skills.sh, which leaves the forks' recorded base alone.
set -euo pipefail

src=${1:?usage: skills-drift.sh <upstream-clone> [<commit>]}
ref=${2:-HEAD}
root=$(cd "$(dirname "$0")/.." && pwd)
vendor=internal/skills/vendor
prov=$root/$vendor/PROVENANCE.json
git -C "$src" rev-parse --verify --quiet "$ref^{commit}" >/dev/null || { echo "no commit $ref in $src" >&2; exit 2; }
cand=$(git -C "$src" rev-parse "$ref")
echo "pinned    $(jq -r .commit "$prov")"
echo "candidate $cand"

sha() { openssl dgst -sha256 -r | cut -d' ' -f1; }
# the candidate's copy of a vendored path (<skill>/<rest>), flattened as vendor-skills.sh does
cand_file() {
  local group
  for group in engineering productivity; do
    git -C "$src" show "$cand:skills/$group/$1" 2>/dev/null && return 0
  done
  return 1
}
# the recorded base: the vendored upstream file, from git when the working tree was already re-vendored
base_file() {
  local want=$2 got
  if got=$(git -C "$root" show "HEAD:$vendor/upstream/$1" 2>/dev/null; echo x) && [ "$(printf %s "${got%x}" | sha)" = "$want" ]; then
    printf %s "${got%x}"; return 0
  fi
  if [ -f "$root/$vendor/upstream/$1" ] && [ "$(sha <"$root/$vendor/upstream/$1")" = "$want" ]; then
    cat "$root/$vendor/upstream/$1"; return 0
  fi
  return 1
}
cand_files() { # candidate files of a skill, relative to skills/
  local group
  for group in engineering productivity; do
    git -C "$src" ls-tree -r --name-only "$cand" "skills/$group/$1/" 2>/dev/null | sed "s|^skills/$group/||"
  done
}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/skills-drift.XXXXXX"); trap 'rm -rf "$tmp"' EXIT
changed=0
for fork in $(jq -r '.forks[].name' "$prov"); do
  status=""
  report=""
  while IFS=$'\t' read -r rel want; do
    if ! cand_file "$rel" >"$tmp/new" 2>/dev/null; then
      status="changed"; report+="--- $rel: removed upstream"$'\n'; continue
    fi
    [ "$(sha <"$tmp/new")" = "$want" ] && continue
    status="changed"
    if base_file "$rel" "$want" >"$tmp/old"; then
      report+=$(diff -u --label "base/$rel" --label "candidate/$rel" "$tmp/old" "$tmp/new" || true)$'\n'
    else
      report+="--- $rel: changed upstream (recorded base not recoverable to diff)"$'\n'
    fi
  done < <(jq -r --arg n "$fork" '.forks[] | select(.name == $n) | .upstream_files | to_entries[] | [.key, .value] | @tsv' "$prov")
  while read -r rel; do
    [ -n "$rel" ] || continue
    jq -e --arg n "$fork" --arg r "$rel" '.forks[] | select(.name == $n) | .upstream_files[$r]' "$prov" >/dev/null && continue
    status="changed"; report+="+++ $rel: added upstream"$'\n'
  done < <(cand_files "$fork" | sort)
  if [ -n "$status" ]; then
    changed=1
    echo "CHANGED   $fork"
    printf '%s\n' "$report"
  else
    echo "unchanged $fork"
  fi
done
exit $changed
