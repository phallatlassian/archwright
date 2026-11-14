#!/bin/sh
# Stands in for the codex CLI in doctor tests; see stub-claude.sh. Codex reports
# no denials as events, so "deny" makes the call fail the way a refused command does.
case "$1" in
--version) echo "codex-cli 0.162.1"; exit 0 ;;
exec) [ "$2" = "--help" ] && { echo "  --ephemeral"; exit 0; } ;;
esac
mode=${STUB_MODE:-$(cat "$STUB_MODE_FILE" 2>/dev/null)}
echo '{"type":"thread.started","thread_id":"t"}'
echo '{"type":"turn.started"}'
if [ "$mode" = "silent" ]; then
	echo '{"type":"item.completed","item":{"id":"i","type":"agent_message","text":"I did not run it"}}'
	echo '{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}'
	exit 0
fi
id=req-stub
printf '{"id":"%s","token":"%s","tool":"doctor_ping"}' "$id" "$ARCHWRIGHT_RUN_TOKEN" > "$ARCHWRIGHT_SPOOL/.tmp-$id"
mv "$ARCHWRIGHT_SPOOL/.tmp-$id" "$ARCHWRIGHT_SPOOL/$id.req.json"
n=0
while [ ! -f "$ARCHWRIGHT_SPOOL/$id.resp.json" ] && [ $n -lt 200 ]; do sleep 0.05; n=$((n+1)); done
out=$(sed 's/"/\\"/g' "$ARCHWRIGHT_SPOOL/$id.resp.json" 2>/dev/null)
printf '{"type":"item.completed","item":{"id":"i","type":"agent_message","text":"%s"}}\n' "$out"
echo '{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}'
