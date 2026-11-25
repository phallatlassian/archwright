#!/usr/bin/env bash
# A scripted stand-in for the interactive claude CLI. It takes claude's flags,
# plays the hooks claude would send by running the hook command found in the
# --settings file, and accepts input at a permission prompt the way claude
# does: a line of input, Enter, approves it. Steps: stand-in-lib.sh.
# What it saw is left in $ARCHWRIGHT_STANDIN_OUT: args (one per line), pwd, env, tool.out.
set -uf

if [ "${1:-}" = "--version" ]; then echo "stand-in 0.0.1"; exit 0; fi

here=$(cd "$(dirname "$0")" && pwd)
out="${ARCHWRIGHT_STANDIN_OUT:-}"
settings="" sid="" brief=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  case "${args[i]}" in
    --settings) settings="${args[i + 1]}" ;;
    --session-id) sid="${args[i + 1]}" ;;
    --) brief="${args[i + 1]:-}"; break ;;
  esac
done
if [ -n "$out" ]; then
  mkdir -p "$out"
  printf '%s\n' "$@" >"$out/args"
  pwd -P >"$out/pwd"
  env >"$out/env"
fi

# The first "command" in the settings file is the hook command; hooks all share it.
hookcmd=$(sed -n 's/^ *"command": "\(.*\)",\{0,1\}$/\1/p' "$settings" | head -1)
queuefile=/dev/null

# shellcheck source=stand-in-lib.sh
. "$here/stand-in-lib.sh"
run_steps
