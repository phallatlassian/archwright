#!/bin/sh
# Stub git for tests: records its argv, one call per line, in $STUB_GIT_LOG and answers by $STUB_GIT_MODE: ok (default) | auth | network | rejected.
[ -n "$STUB_GIT_LOG" ] && echo "$*" >> "$STUB_GIT_LOG"
case "${STUB_GIT_MODE:-ok}" in
ok) ;;
auth) echo "fatal: could not read Username for 'https://github.com': terminal prompts disabled" >&2; exit 128 ;;
network) echo "fatal: unable to access 'https://github.com/o/r.git/': Could not resolve host: github.com" >&2; exit 128 ;;
rejected) echo " ! [rejected]        a -> a (non-fast-forward)" >&2; exit 1 ;;
esac
