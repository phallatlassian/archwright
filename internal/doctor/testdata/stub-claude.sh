#!/bin/sh
# Stands in for the claude CLI in doctor tests. As the "agent" it makes the
# probe's one `archwright tool doctor_ping` call by writing the request into
# the spool and waiting for the response, then reports it as claude's stream-json.
# $STUB_MODE (or the contents of the file $STUB_MODE_FILE): ok (default) | deny (the call is denied, never made) | silent (never made)
case "$1" in
--version) echo "2.1.285 (Claude Code)"; exit 0 ;;
--help) echo "  --no-session-persistence"; exit 0 ;;
esac
echo '{"type":"system","subtype":"init","session_id":"s","permissionMode":"dontAsk"}'
mode=${STUB_MODE:-$(cat "$STUB_MODE_FILE" 2>/dev/null)}
case "$mode" in
deny)
	echo '{"type":"result","subtype":"success","is_error":false,"result":"denied","permission_denials":[{"tool_name":"Bash","tool_use_id":"t1","tool_input":{"command":"archwright tool doctor_ping"}}]}'
	exit 0 ;;
silent)
	echo '{"type":"result","subtype":"success","is_error":false,"result":"I did not run it","permission_denials":[]}'
	exit 0 ;;
esac
id=req-stub
printf '{"id":"%s","token":"%s","tool":"doctor_ping"}' "$id" "$ARCHWRIGHT_RUN_TOKEN" > "$ARCHWRIGHT_SPOOL/.tmp-$id"
mv "$ARCHWRIGHT_SPOOL/.tmp-$id" "$ARCHWRIGHT_SPOOL/$id.req.json"
n=0
while [ ! -f "$ARCHWRIGHT_SPOOL/$id.resp.json" ] && [ $n -lt 200 ]; do sleep 0.05; n=$((n+1)); done
if [ "$mode" = "norespond" ] || [ ! -f "$ARCHWRIGHT_SPOOL/$id.resp.json" ]; then
	echo '{"type":"result","subtype":"success","is_error":false,"result":"no answer","permission_denials":[]}'
	exit 0
fi
out=$(sed 's/"/\\"/g' "$ARCHWRIGHT_SPOOL/$id.resp.json")
printf '{"type":"result","subtype":"success","is_error":false,"result":"%s","permission_denials":[]}\n' "$out"
