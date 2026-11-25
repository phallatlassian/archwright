#!/bin/sh
# Stands in for tmux in doctor tests: it reports a version and starts and stops
# servers without making a socket, so the check needs neither.
case "$1" in
-V) echo "tmux 3.8" ;;
esac
exit 0
