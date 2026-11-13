#!/bin/sh
# Stands in for the claude CLI in tests: replays $STUB_FIXTURE (a file in this
# directory) as its stdout stream, recording how it was called.
dir=$(dirname "$0")
if [ "$1" = "--version" ]; then echo "2.1.285 (Claude Code)"; exit 0; fi
[ -n "$STUB_ARGS" ] && printf '%s\n' "$@" > "$STUB_ARGS"
[ -n "$STUB_STDIN" ] && cat | wc -c > "$STUB_STDIN"
[ -n "$STUB_PWD" ] && pwd > "$STUB_PWD"
cat "$dir/$STUB_FIXTURE"
[ "$STUB_HOLD" = 1 ] && sleep 60
exit 0
