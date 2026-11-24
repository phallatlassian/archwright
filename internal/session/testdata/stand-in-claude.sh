#!/usr/bin/env bash
# A scripted stand-in for the interactive claude CLI. It takes claude's flags,
# plays the hooks claude would send by running the hook command found in the
# --settings file, and accepts input at a permission prompt the way claude
# does: a line of input, Enter, approves it. What it plays is
# $ARCHWRIGHT_STANDIN, steps separated by ";":
#   hook <Event> [tool [command]]  run the hook command with a payload
#   tool <name> <json>             call a Works tool: `$ARCHWRIGHT_BIN tool <name>` with the JSON (no spaces) on stdin
#   prompt                         print a permission prompt and read one line
#   sleep <seconds>
#   hold                           stay open until killed
#   exit <status>
# What it saw is left in $ARCHWRIGHT_STANDIN_OUT: args (one per line), pwd, env, tool.out.
set -uf

if [ "${1:-}" = "--version" ]; then echo "stand-in 0.0.1"; exit 0; fi

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

turn=0
hook() { # event [tool [command...]]
  local event="$1" tool="${2:-}" cmd="${*:3}"
  [ "$event" = UserPromptSubmit ] && turn=$((turn + 1))
  printf '{"hook_event_name":"%s","session_id":"%s","transcript_path":"/tmp/stand-in-%s.jsonl","cwd":"%s","permission_mode":"default","turn_id":"turn-%s","tool_name":"%s","tool_input":{"command":"%s"},"tool_use_id":"tu-%s"}' \
    "$event" "$sid" "$sid" "$(pwd -P)" "$turn" "$tool" "$cmd" "$tool" | eval "$hookcmd"
}

IFS=';' read -ra steps <<<"${ARCHWRIGHT_STANDIN:-hook SessionStart;hook UserPromptSubmit;hook Stop;hold}"
for step in "${steps[@]}"; do
  # shellcheck disable=SC2086
  set -- $step
  case "${1:-}" in
    hook) shift; hook "$@" ;;
    tool)
      dest=/dev/null
      [ -z "$out" ] || dest="$out/tool.out"
      printf '%s' "${3:-}" | "$ARCHWRIGHT_BIN" tool "$2" >"$dest" 2>&1 ;;
    prompt)
      echo "Do you want to proceed? 1. Yes 2. No"
      if read -r _; then [ -z "$out" ] || echo approved >"$out/answered"; fi ;;
    sleep) sleep "$2" ;;
    hold) while :; do sleep 1; done ;;
    exit) exit "${2:-0}" ;;
  esac
done
