# Shared by the scripted stand-ins for the interactive claude and codex CLIs.
# The caller sets: sid (the vendor session id its hooks report), hookcmd (the
# command the vendor was configured to run for a hook), out
# ($ARCHWRIGHT_STANDIN_OUT) and queuefile (where `codex queue` leaves messages),
# then calls run_steps. What it plays is $ARCHWRIGHT_STANDIN, steps separated by ";":
#   hook <Event> [tool [command]]  run the hook command with a payload
#   again                          deliver the last hook's payload a second time (a duplicate delivery)
#   tool <name> <json>             call a Works tool: `$ARCHWRIGHT_BIN tool <name>` with the JSON (no spaces) on stdin
#   prompt                         print a permission prompt and read one line
#   readprompt                     read one line typed at the composer (a Nudge: pasted text, then Enter) into $out/prompt.in
#   readnudge                      readprompt for a Nudge that points at a file: take the `export` lines of that file into the environment, as an agent told to would
#   waitqueue                      stay busy until a message has been queued for this session
#   sleep <seconds>
#   hold                           stay open until killed
#   exit <status>

turn=0
last=""
hook() { # event [tool [command...]]
  local event="$1" tool="${2:-}" cmd="${*:3}"
  [ "$event" = UserPromptSubmit ] && turn=$((turn + 1))
  last=$(printf '{"hook_event_name":"%s","session_id":"%s","transcript_path":"/tmp/stand-in-%s.jsonl","cwd":"%s","permission_mode":"default","turn_id":"turn-%s","tool_name":"%s","tool_input":{"command":"%s"},"tool_use_id":"tu-%s"}' \
    "$event" "$sid" "$sid" "$(pwd -P)" "$turn" "$tool" "$cmd" "$tool")
  printf '%s' "$last" | eval "$hookcmd"
}

run_steps() {
  local steps step
  IFS=';' read -ra steps <<<"${ARCHWRIGHT_STANDIN:-hook SessionStart;hook UserPromptSubmit;hook Stop;hold}"
  for step in "${steps[@]}"; do
    # shellcheck disable=SC2086
    set -- $step
    case "${1:-}" in
      hook) shift; hook "$@" ;;
      again) printf '%s' "$last" | eval "$hookcmd" ;;
      tool)
        dest=/dev/null
        [ -z "$out" ] || dest="$out/tool.out"
        printf '%s' "${3:-}" | "$ARCHWRIGHT_BIN" tool "$2" >"$dest" 2>&1 ;;
      prompt)
        echo "Do you want to proceed? 1. Yes 2. No"
        if read -r _; then [ -z "$out" ] || echo approved >"$out/answered"; fi ;;
      readprompt)
        if read -r line; then [ -z "$out" ] || printf '%s\n' "$line" >>"$out/prompt.in"; fi ;;
      readnudge)
        if read -r line; then
          [ -z "$out" ] || printf '%s\n' "$line" >>"$out/prompt.in"
          nudgefile=${line#Read the file }
          nudgefile=${nudgefile% in full and carry out what it says.}
          # shellcheck disable=SC2046
          eval "$(grep '^export ' "$nudgefile")"
        fi ;;
      waitqueue) while [ ! -s "$queuefile" ]; do sleep 0.05; done ;;
      sleep) sleep "$2" ;;
      hold) while :; do sleep 1; done ;;
      exit) exit "${2:-0}" ;;
    esac
  done
}
