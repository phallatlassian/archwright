#!/usr/bin/env bash
# TESTING ONLY. Wipe the Works' data and restart it, so a shakedown starts clean.
# Never run by the Works or an agent: run it yourself.
#
#   scripts/reset-works.sh              wipe every Job; keep Sites, their approved gates and the clones
#   scripts/reset-works.sh --all        also wipe Sites, gate approvals, clones and database backups
#   scripts/reset-works.sh --dry-run    print what would go and change nothing
#   scripts/reset-works.sh --yes        do not ask first
#
# It stops the Works, empties the database (or deletes it with --all), removes
# the event log, run records and worktrees, prunes the Job branches from the
# clones, and starts the Works again. It does not touch the Forge: PRs and
# pushed branches on GitHub stay. ARCHWRIGHT_BIN names the binary (default
# archwright); ARCHWRIGHT_STATE_HOME / XDG_STATE_HOME are honoured like the
# Works does.
set -euo pipefail

all=0 yes=0 dry=0
for a in "$@"; do
  case $a in
    --all) all=1 ;;
    --yes | -y) yes=1 ;;
    --dry-run | -n) dry=1 ;;
    -h | --help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "unknown option: $a (see --help)" >&2; exit 2 ;;
  esac
done

bin=${ARCHWRIGHT_BIN:-archwright}
command -v "$bin" >/dev/null || { echo "no '$bin' on PATH (set ARCHWRIGHT_BIN)" >&2; exit 1; }

if [ -n "${ARCHWRIGHT_STATE_HOME:-}" ]; then
  state=$ARCHWRIGHT_STATE_HOME
elif [ "${XDG_STATE_HOME:-}" != "" ] && [ "${XDG_STATE_HOME#/}" != "$XDG_STATE_HOME" ]; then
  state=$XDG_STATE_HOME/archwright
else
  state=$HOME/.local/state/archwright
fi
db=$state/archwright.db
[ -d "$state" ] || { echo "no state directory at $state: nothing to reset" >&2; exit 1; }

# Tables a default reset keeps: the registered Sites and what approves their gates.
keep="sites gate_changes approval_credential"

echo "state directory: $state"
if [ "$all" = 1 ]; then
  echo "will delete:     the database and its backups, events.log, runs/, worktrees/, clones/"
else
  echo "will empty:      every table except: $keep"
  echo "will delete:     events.log, runs/, worktrees/, and the archwright/* branches in clones/"
fi
echo "will restart:    the Works (running Wrights are stopped)"
if [ "$dry" = 1 ]; then echo "dry run: nothing changed"; exit 0; fi

if [ "$yes" != 1 ]; then
  read -r -p "Type 'reset' to continue: " ans
  [ "$ans" = "reset" ] || { echo "cancelled"; exit 1; }
fi

# Stop the Works and wait for the process to be gone: the database must not be
# touched while it holds the write-ahead log.
pid=$(sed -n 's/.*"pid": *\([0-9][0-9]*\).*/\1/p' "$state/works.pid" 2>/dev/null || true)
echo "stopping the Works"
"$bin" works stop >/dev/null 2>&1 || true
for _ in $(seq 1 50); do
  { [ -z "$pid" ] || ! kill -0 "$pid" 2>/dev/null; } && break
  sleep 0.2
done
if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
  echo "the Works (pid $pid) did not stop; nothing was deleted" >&2
  exit 1
fi

if [ "$all" = 1 ]; then
  rm -f "$db" "$db-wal" "$db-shm" "$state"/archwright.db.v*.bak
else
  command -v sqlite3 >/dev/null || { echo "sqlite3 not found: use --all to delete the database instead" >&2; exit 1; }
  if [ -f "$db" ]; then
    # events is append-only: a trigger refuses DELETE. Drop it inside the transaction and put it
    # back from its stored definition, so a failure rolls both back.
    trig=$(sqlite3 "$db" "SELECT sql FROM sqlite_master WHERE type='trigger' AND name='events_no_delete'")
    sql="PRAGMA foreign_keys=OFF; BEGIN;"
    [ -z "$trig" ] || sql="$sql DROP TRIGGER events_no_delete;"
    for t in $(sqlite3 "$db" "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"); do
      case " $keep " in *" $t "*) continue ;; esac
      sql="$sql DELETE FROM \"$t\";"
    done
    if [ "$(sqlite3 "$db" "SELECT count(*) FROM sqlite_master WHERE name='sqlite_sequence'")" != "0" ]; then
      sql="$sql DELETE FROM sqlite_sequence WHERE name NOT IN ('sites','gate_changes','approval_credential');"
    fi
    [ -z "$trig" ] || sql="$sql $trig;"
    sqlite3 "$db" "$sql COMMIT; VACUUM;"
    echo "emptied the database"
  fi
fi

rm -f "$state/events.log" "$state/events.log.1"
: >"$state/works.log" 2>/dev/null || true
rm -rf "$state/runs" "$state/worktrees"

if [ "$all" = 1 ]; then
  rm -rf "$state/clones"
else
  for c in "$state"/clones/*; do
    [ -d "$c" ] || continue
    git -C "$c" worktree prune
    git -C "$c" for-each-ref --format='%(refname)' refs/heads/archwright/ |
      while read -r ref; do git -C "$c" update-ref -d "$ref"; done
  done
fi

echo "starting the Works"
"$bin" job list >/dev/null
"$bin" works status
cat <<'EOF'

Done. Restart your Archwright session too: it registered for idle-wake nudges with
the old Works at start-up, and a restarted Works does not know it.
Branches and PRs already pushed to the Forge are untouched.
EOF
