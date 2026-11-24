#!/usr/bin/env bash
# A scripted stand-in for the interactive codex CLI. It takes codex's flags,
# picks its own session id as codex does, plays the hooks codex would send by
# running the hook command found in $CODEX_HOME/hooks.json (codex's stable,
# file-based hook source), and accepts input at a permission prompt the way
# codex does. Steps: stand-in-lib.sh.
#
# `stand-in-codex.sh queue --thread <id> --message <text>` is `codex queue`: it
# leaves the message in $CODEX_HOME/queue, which a running session sees, and
# the arguments in $CODEX_HOME/queue-args.
# What a session saw is left in $ARCHWRIGHT_STANDIN_OUT: args (one per line), pwd, env, tool.out.
set -uf

if [ "${1:-}" = "--version" ]; then echo "codex-cli stand-in 0.0.1"; exit 0; fi

home="${CODEX_HOME:-$HOME/.codex}"
if [ "${1:-}" = "queue" ]; then
  printf '%s\n' "$@" >"$home/queue-args"
  shift
  while [ $# -gt 0 ]; do
    [ "$1" = --message ] && printf '%s\n' "$2" >>"$home/queue"
    shift
  done
  exit 0
fi

here=$(cd "$(dirname "$0")" && pwd)
out="${ARCHWRIGHT_STANDIN_OUT:-}"
sid="codex-$$-$RANDOM" brief=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  case "${args[i]}" in
    --) brief="${args[i + 1]:-}"; break ;;
  esac
done
if [ -n "$out" ]; then
  mkdir -p "$out"
  printf '%s\n' "$@" >"$out/args"
  pwd -P >"$out/pwd"
  env >"$out/env"
fi

hookcmd=$(sed -n 's/^ *"command": "\(.*\)",\{0,1\}$/\1/p' "$home/hooks.json" | head -1)
queuefile="$home/queue"

# shellcheck source=stand-in-lib.sh
. "$here/stand-in-lib.sh"
run_steps
