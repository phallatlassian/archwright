#!/bin/sh
# Stands in for the claude CLI in tests: replays $ARCHWRIGHT_STUB_FIXTURE (a file in this
# directory) as its stdout stream, recording how it was called.
dir=$(dirname "$0")
if [ "$1" = "--version" ]; then echo "2.1.285 (Claude Code)"; exit 0; fi
[ -n "$ARCHWRIGHT_STUB_ARGS" ] && printf '%s\n' "$@" > "$ARCHWRIGHT_STUB_ARGS"
[ -n "$ARCHWRIGHT_STUB_STDIN" ] && cat | wc -c > "$ARCHWRIGHT_STUB_STDIN"
[ -n "$ARCHWRIGHT_STUB_PWD" ] && pwd > "$ARCHWRIGHT_STUB_PWD"
cat "$dir/$ARCHWRIGHT_STUB_FIXTURE"
[ "$ARCHWRIGHT_STUB_HOLD" = 1 ] && sleep 60
exit 0
