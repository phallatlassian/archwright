#!/bin/sh
# Stands in for the claude CLI in tests: replays $ARCHWRIGHT_STUB_FIXTURE (a file in this
# directory) as its stdout stream, recording how it was called.
dir=$(dirname "$0")
if [ "$1" = "--version" ]; then echo "2.1.285 (Claude Code)"; exit 0; fi
[ -n "$ARCHWRIGHT_STUB_ARGS" ] && printf '%s\n' "$@" > "$ARCHWRIGHT_STUB_ARGS"
[ -n "$ARCHWRIGHT_STUB_STDIN" ] && cat | wc -c > "$ARCHWRIGHT_STUB_STDIN"
[ -n "$ARCHWRIGHT_STUB_PWD" ] && pwd > "$ARCHWRIGHT_STUB_PWD"
[ -n "$ARCHWRIGHT_STUB_ENV" ] && env | grep '^ARCHWRIGHT_RUN\|^ARCHWRIGHT_SPOOL\|^GIT_CONFIG_GLOBAL' > "$ARCHWRIGHT_STUB_ENV"
cat "$dir/$ARCHWRIGHT_STUB_FIXTURE"
# $ARCHWRIGHT_STUB_WAITFOR: stay alive until that file exists, so a test can play the agent's tool calls
[ -n "$ARCHWRIGHT_STUB_WAITFOR" ] && while [ ! -e "$ARCHWRIGHT_STUB_WAITFOR" ]; do sleep 0.05; done
[ "$ARCHWRIGHT_STUB_HOLD" = 1 ] && sleep 60
exit 0
