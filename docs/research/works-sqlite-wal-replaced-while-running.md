# What replaced the Works' SQLite WAL while it was running? (#135, from #131)

Researched 2026-11-26 against `main` at 38329c2. Primary sources only: this repo's code and ADRs, sqlite.org documentation, and the modernc.org/sqlite v1.60.1 source in the module cache (`$(go env GOMODCACHE)/modernc.org/sqlite@v1.60.1`). I ran on Linux amd64. I cannot inspect the developer's macOS machine, so everything that depends on what happened there is marked UNVERIFIED.

## Conclusion

The sources establish a cause that fits every known fact, and it is a bug in the Works itself, not in `archwright approval set` or in a foreign tool.

**The Works runs `archwright doctor` inside its own process, and doctor's `OpenSnapshot` does an `os.Open` + `Close` on the live `archwright.db`, `-wal` and `-shm`. On POSIX, `close()` drops every advisory lock the process holds on that file. That strips the Works' own SQLite locks. After that, any other read-write opener of the database (for example `archwright approval set`, or a non-`-readonly` `sqlite3`) believes it is the only connection, and on close it checkpoints the WAL into the main file and unlinks the WAL and shm. The Works keeps writing to the unlinked files.**

Chain, with sources:

1. `ServeWith` is started with `Preflight: preflight(dirs)` (`cmd/archwright/main.go:543`), and `preflight` runs `doctor.Run` (`cmd/archwright/preflight.go:15-17`). `Runs.StartFor` calls it before every run starts (`internal/works/runs.go:225-233`). The gate remembers a pass for 10 minutes and a failure for 20 seconds, then runs doctor again (`internal/doctor/gate.go:38-62`). This all executes in the `works serve` process.
2. `doctor.Run` calls `checkSchema` (`internal/doctor/doctor.go:116`), which calls `store.OpenSnapshot(db)` (`internal/doctor/checks_state.go:58`). `OpenSnapshot` calls `copyFile` on `path-wal`, `path-shm` and `path` (`internal/store/snapshot.go:34-41`), and `copyFile` does `os.Open(from)` then `in.Close()` (`snapshot.go:77-90`). That is a bare open/read/close of a database file the same process has open through SQLite.
3. SQLite documents this exact hazard: "the close() system call will cancel all POSIX advisory locks on the same file for all threads and all file descriptors in the process ... Those other threads have no way of knowing that their locks have just been trashed" (https://www.sqlite.org/howtocorrupt.html, section 2.2). The driver says the same, and that OFD locks, which survive such a close, exist only on Linux and are off by default (`modernc.org/sqlite@v1.60.1/ofd.go:9-60`). So on macOS there is no switch to turn on.
4. A WAL-mode connection keeps its SHARED lock on the main file for its whole life: the WAL branch of `pager_unlock` ends the read transaction and never calls `pagerUnlockDb` (`lib/sqlite_g_0000000000060000.go:103059-103075`). It also keeps its shared-memory (DMS) lock for life. Once `close()` drops them, nothing re-takes them.
5. When the last connection closes, SQLite tries an EXCLUSIVE lock on the main file; if it gets it, it checkpoints and then unlinks the WAL and shm (`lib/sqlite_g_0000000000060000.go:114017-114065`; https://www.sqlite.org/wal.html sections 4 and 6). With the Works' locks gone, a second process gets it. That checkpoint is the main-file write at 20:27:07; the unlink leaves the Works holding the orphaned WAL inode (2,130,072 bytes by `lsof`); the next opener creates a fresh 0-byte WAL and shm (20:28, 20:28:10).

This also explains why the earlier repros did not reproduce it: they opened the second process against a healthy writer. They never put the writer through an in-process open/close of its own files first.

## Reproduction (Linux amd64, modernc v1.60.1, SQLite 3.53.4 per the driver CHANGELOG)

I wrote a small Go program in a scratch directory outside the repo (never against `~/.local/state`). Process A is the "Works": it opens a temp database with the same DSN as `store.Open` (`journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(1)`, `SetMaxOpenConns(1)`), inserts 50 rows, optionally does the `OpenSnapshot` copy's open/close on `-wal`, `-shm` and the main file, runs child process B (a second modernc process that opens, optionally inserts one row, and closes), inserts 50 more rows, and then a fresh process C opens the files on disk and counts rows, standing in for a restart.

| A's in-process open/close of its own files | B | Result |
|---|---|---|
| none (control) | read-write, writes 1 row | WAL and shm keep their inodes; C sees 101 rows (A: 101) |
| main + `-wal` + `-shm` (what `OpenSnapshot` does) | read-write, writes 1 row | after B, WAL and shm are gone; A's fds show `t.db-wal (deleted)` and `t.db-shm (deleted)`; A sees 101 rows; **C sees 51: the 50 rows A wrote after B are lost** |
| same | read-write, only `SELECT count(*)` | C sees 50 rows: a plain read-write open and close is enough |
| same | `mode=ro` (like `sqlite3 -readonly`) | WAL and shm untouched; A's later writes survive (C sees 100) |
| main only, `-wal` only, `-shm` only, `-wal`+`-shm` | read-write | no loss |
| main + `-shm` | read-write | loss (51 rows): the main-file lock and the shm lock are both needed |

So the second process must be a read-write opener; a read-only one cannot take the EXCLUSIVE lock to checkpoint and delete, which matches the triage result that `sqlite3 -readonly` did nothing. SQLite 3.51.0 added "additional defenses" against broken locks, but the page says "they are not a cure-all" (howtocorrupt section 2.2); the repro uses a build newer than that and still loses data. The Linux repro uses POSIX fcntl locks, the same kind the driver uses on macOS (`ofd.go:9-25`), but I did not run it on macOS: UNVERIFIED there.

The program is about 100 lines; it is not committed. A follow-up ticket should add it as a store test (see tickets).

## Every process that can open, move, copy or delete the database or its WAL/shm

| # | Process | What it does to the files | Source | Fits the facts? |
|---|---|---|---|---|
| 1 | `works serve` (the Works) | Opens read-write for life (`store.Open`); `Close` and clean-stop `Checkpoint`; and, through the doctor preflight, opens and closes all three files (the culprit above) | `internal/works/works.go:92-107`, `internal/store/store.go:90-115`, `snapshot.go:34-41` | Yes: the precondition |
| 2 | `archwright approval set` | `store.Open` then `Close` on the live database; the one direct opener in `cmd/` | `cmd/archwright/main.go:270-298` | Yes, as the second process. The issue says it "may" have been run near 20:27; 20:27:07 is the main file's last write. UNVERIFIED |
| 3 | `archwright doctor` (CLI) | `OpenSnapshot` copies the files into a temp dir and opens the copy. Opens the live files for reading in its own process only, so it strips only its own locks (none) | `cmd/archwright/main.go:94-96`, `snapshot.go` | No effect on the Works. It never takes SQLite locks on the live files |
| 4 | `sqlite3` or any SQLite client the developer or an agent runs | Read-write open then close behaves like #2; `-readonly` does not (repro) | repro; https://www.sqlite.org/wal.html section 4 | Candidate for B. The issue records `-readonly` reads between 20:20 and 20:35 from a Claude session; a read-write one is UNVERIFIED |
| 5 | `store.Open` backup path (`VACUUM INTO`) | Runs only when migrating an existing database below the latest version; writes a separate `.bak`; `pruneBackups` deletes only `*.bak` | `store.go:127-141, 180-215` | No: it does not touch the WAL, and there was no schema change at 20:28 (the rebuild and restart were at 21:10) |
| 6 | Another `works serve` | Blocked by `flock` on `works.lock` before `store.Open` | `works.go:75-85` | No |
| 7 | `scripts/reset-works.sh` | Deletes db, wal and shm (`--all`) or runs `sqlite3` against the database, after stopping the Works. First committed 2026-11-23 | `scripts/reset-works.sh:75-93`, `git log` | No: it did not exist on 11-22, and writes continued after 20:28 |
| 8 | Tests (`go test`, for example a gate run in a Site that is Archwright itself) | Tests use temp dirs (`newSandbox` sets `ARCHWRIGHT_STATE_HOME`: `cmd/archwright/main_test.go:49-55`); I found none that open the real state directory | grep of `*_test.go` | Unlikely; not exhaustively proven |
| 9 | `cmd/dashdev` | Mock data only, no database | `cmd/dashdev/mock.go` | No |
| 10 | Time Machine, Spotlight, sync tools, editors | Read, or snapshot, `~/.local/state`. Nothing I could find in primary sources unlinks or replaces a live file this way. A backup tool that copies without the WAL is the howtocorrupt 1.2 problem, but it does not delete the original | https://www.sqlite.org/howtocorrupt.html 1.2 | UNVERIFIED on macOS; no evidence for it |
| 11 | Seatbelt or file-provider policies | I found no documented mechanism that unlinks files the owner has open. An agent sandbox denies writes rather than unlinking | none | UNVERIFIED; cannot test on macOS |
| 12 | An agent or the developer running `rm`, `mv`, `git clean` or `sqlite3 .recover` in the state directory | Would unlink the files directly | (no code; none found in the repo's own commands) | Cannot be ruled out; see capture list |

No code path in the Works unlinks or renames the database, WAL or shm directly (grep of `os.Remove`, `RemoveAll` and `os.Rename` across `internal/` and `cmd/`: nothing touches those names; `pruneBackups` removes `*.bak` only).

## Assessment against the known facts

- Timeline, "state came back as of about 20:27": matches a checkpoint by the closing second process at 20:27:07 (main file mtime), with every later commit in the orphan.
- 0-byte WAL (mtime 20:28) and shm (20:28:10) while the Works holds a 2.1 MB WAL: the old files were unlinked, so the path names new files created by a later opener. Truncating the same inode would show 0 bytes in `lsof` too, so the inode comparison matters (UNVERIFIED: the issue does not record the path's inode next to 200016191). My repro shows `(deleted)` for the held WAL and shm.
- Main file not checkpointed after 20:27:07: yes, the one checkpoint is the second process's close-time checkpoint; the Works never checkpoints between starts (`Checkpoint` runs only at clean stop, `store/walguard.go:196-211`).
- "A second modernc process open, write, close did not disturb the WAL" and "`sqlite3 -readonly` did not": both repros match the control and `mode=ro` rows of my table. They lacked the preceding in-process open/close.
- "Doctor ruled out": that held for the `archwright doctor` command line, not for the same code running inside the Works.
- 2,130,072 bytes is 32 + 517 x (24 + 4096): consistent with a 4096-byte page size (the page size itself is UNVERIFIED).

### What this does not prove

I cannot show which preflight ran just before 20:27:07 or which process was the second opener. The ranking of the remaining uncertainty is below.

1. (established mechanism) Works-internal lock loss from the doctor preflight, plus any read-write opener. Everything fits.
2. A different unlink with no lock loss: `rm`/`mv` by a person or agent, or an unknown tool. Fits the unlink but not the checkpoint at 20:27:07, which an `rm` does not produce. Less likely.
3. macOS-specific behaviour: no evidence.

### Evidence to capture on the developer's machine if it recurs

- Before restarting the Works: `lsof -p <works pid>` and `lsof +L1 | grep archwright` (open files with link count 0 show the orphan); `stat -f '%i %z %Sm' archwright.db*` to compare inodes with the ones `lsof` shows.
- Whether a run started in the minutes before the loss (a preflight): `events.log` and `works.log` lines near the main file's last mtime, and the doctor-run timing.
- Which second process opened the database: shell history with timestamps (`approval set`, `sqlite3`), the Claude session's tool-call transcript, `works.log`, and `log show --start ... --predicate 'process == "sqlite3" OR process == "archwright"'` on macOS (unified log; availability for file opens UNVERIFIED).
- Run `fs_usage -w -f filesys archwright sqlite3` during a shakedown to see opens and unlinks of the three files.

## Would the #131 defensive checks have caught it?

Partly. They are implemented (`internal/store/walguard.go`, ADR 0002 amendment).

- Detection: yes. The guard records dev and inode of `-wal` and `-shm` at open and re-stats them before each write and every 5 seconds (`walguard.go:12-21, 62-93`, `store.go:106-110`). After the unlink the stat fails (`known` file now missing) or the inode differs, so the guard trips, writes are refused, and `works.go:176-180` logs FATAL and stops the Works. The loss would have been the commits in at most about 5 seconds (and any between the unlink and the next write), not 50 minutes.
- Recovery: no. The commits already in the orphaned WAL are not recovered; `Close` deliberately leaves the handle alone after a trip (`store.go:118-135`). The clean-stop checkpoint also refuses.
- Prevention: no. It does not remove the cause, so the Works would stop itself after a preflight plus a second opener on every occurrence. That would make `approval set` after any run start a reliable way to crash the Works.
- The startup `events.log` comparison and the Needs-you entry in the #131 brief: UNVERIFIED whether they shipped; the 5-second guard is.

## Recommendation: broader than one command, but the main fix is in doctor

Moving `approval set` behind the Works API would not fix this: it removes one opener but any `sqlite3` read-write open, or a future command, would still find the Works with no locks. The defect is that the Works opens its own database file outside SQLite. Fix that first.

Suggested tickets:

1. **Stop stripping the Works' locks (the fix).** The Works must never `os.Open`/`Close` its live database, WAL or shm. Make the preflight (and any in-Works doctor run) read the database through the Works' own connection (for example `VACUUM INTO` a temp file, which SQLite documents as a safe live copy, howtocorrupt 1.2), or skip the schema/sites checks that need a copy and use the store directly. Keep `OpenSnapshot` for the CLI `archwright doctor`, which is a separate process. Accept: a test that runs the doctor gate inside a running store and then a second process open/close, and no data loss (use the repro's shape).
2. **Regression test for the lock-stripping class.** Add the repro as a store test (writer, in-process open/close of the files, second read-write opener, then a restart count). It should fail today and pass after ticket 1. Add a lint-style test or comment that no code in the Works process opens the database file by path.
3. **Harden the second-opener side (optional, after 1).** `approval set` through the Works API (ADR 0007 style) so the CLI never opens the live database; this also lets ADR 0002's "nothing else may open `archwright.db`" be literally true for the CLI. Consider a one-line note in ADR 0002 that an in-process copy is also an "open".
4. **Close the detection gap.** Keep the #131 guard; add recovery for a tripped guard (copy the orphan out via `/proc/<pid>/fd` is Linux only, so on macOS prefer a `VACUUM INTO` through the still-open connection at trip time, which reads the orphan's state, written to a rescue file and named in the FATAL log). Reads still answer after a trip (`walguard.go:101-103`), which makes this feasible. UNVERIFIED on macOS that a post-trip `VACUUM INTO` works with the shm unlinked; the Linux repro showed A still reads 101 rows.
5. **Correct the record.** Amend the #131 triage note, "doctor was ruled out", and ADR 0002 text that says doctor "reads a private copy" as if it were harmless in the Works.

Out of scope here, per the brief: implementing any of this.

## Sources

- Repo: `internal/store/store.go`, `snapshot.go`, `walguard.go`; `internal/doctor/{doctor,gate,checks_state}.go`; `internal/works/{works,runs}.go`; `cmd/archwright/{main,preflight}.go`; `scripts/reset-works.sh`; `docs/adr/0002-works-owns-orchestration-state-in-sqlite.md`.
- SQLite: https://www.sqlite.org/howtocorrupt.html (1.2, 1.3, 2.2, 2.5); https://www.sqlite.org/wal.html (sections 4, 5, 6, 7, 9). I did not need `lockingv3.html`: the lock behaviour is in the transpiled source cited above.
- modernc.org/sqlite v1.60.1: `ofd.go`, `CHANGELOG.md` (v1.60.0/1.60.1, SQLite 3.53.4), `lib/sqlite_g_0000000000060000.go` (`_sqlite3WalClose`, `_pager_unlock`).
