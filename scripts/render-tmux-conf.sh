#!/usr/bin/env bash
# Print the tmux config the factory server starts with, to check it.
#
#   scripts/render-tmux-conf.sh
#
# Writes the same <state>/tmux/tmux.conf (and tabs.sh) the Works writes, using
# the same ARCHWRIGHT_*_HOME / XDG_*_HOME / ARCHWRIGHT_TMUX rules. The path goes
# to stderr, the config to stdout. It sources <config>/tmux.conf (default
# ~/.config/archwright/tmux.conf) first; ~/.tmux.conf is never read.
set -euo pipefail
cd "$(dirname "$0")/.."
exec go run ./scripts/render-tmux-conf
