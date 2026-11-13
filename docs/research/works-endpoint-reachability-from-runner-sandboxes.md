# Can a CLI inside each runner's sandbox reach a local Works endpoint? (AW-23)

Run 2026-11-12 on this machine, headless `claude -p` (managed enterprise policy in force, effective mode `auto`) and `codex exec --sandbox workspace-write` (`codex-cli 0.162.1`, managed requirements in force). Facts observed; listener, sockets and throwaway repos were deleted afterwards. No policy files were read.

## Method

A throwaway Python listener served a loopback HTTP port and a Unix domain socket (`/tmp/awrt/w.sock`), logging every request it received; from outside any sandbox both answered `works-ok`. In a throwaway git repo per runner, with `.archwright/run.json` excluded through `.git/info/exclude`, each runner was asked to run three shell commands once and report results: `curl` to the loopback port, `curl --unix-socket` to the socket, and `cat .archwright/run.json` plus writing and reading `.archwright/spool.txt`. For Claude the three commands were pre-allowed with `--allowedTools "Bash(curl:*)" "Bash(cat:*)" "Bash(echo:*)"` for this experiment only, so permission prompts did not confound the result.

## Results

| Transport | `claude -p` (managed) | `codex exec --sandbox workspace-write` |
|---|---|---|
| Loopback HTTP | **Failed**: `curl: (7) Failed to connect ... after 0 ms`; listener saw no request | **Worked**: `works-ok`; listener logged the request |
| Unix domain socket in `/tmp` | **Failed**: `curl: (7) Failed to connect`; listener saw no request | **Failed**: `curl: (7) Failed to connect`; listener saw no request |
| Read `.archwright/run.json` in the working directory | **Worked** (git-excluded file readable) | **Worked** |
| Write and read a spool file in the working directory | **Worked** | **Worked** |

No permission denials were reported by either run. Exit codes of both runner processes were 0.

## Notes and uncertainty

- The Claude failures are consistent with a sandbox network and socket restriction (connection refused at 0 ms to a listener that works outside); the cause was not isolated (no sandbox settings were read or varied), and the Bash tool output did not name a sandbox.
- Codex `workspace-write` is documented as network-off, yet loopback HTTP worked; only the Unix socket was refused. Behavior may differ by version or approval state.
- Only one connection attempt per transport per runner was made; flakiness was not measured.
- The Works watching a spool directory inside a Wright's own worktree (outside the Site, in a Works-managed directory) was not tested, only that the Wright can write and read files in its working directory.
