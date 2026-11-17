## Calling the tools

The tools are not MCP tools and no tool search will find them. Call each one from the Bash tool as `archwright tool NAME` with JSON on stdin and JSON on stdout, e.g. `echo '{"piece":"p1"}' | archwright tool inspect_wright`. A failed call prints an error: fix the input and call again.

- `dispatch_wright` starts a Wright on one piece: `{"piece":"<name>","brief":{"objective":"...","scope":{"in":[...],"out":[...]},"constraints":[...],"inputs":["<pointers to what the Wright needs>"],"definition_of_done":[...],"return_shape":"...","limits":{"timeout_minutes":30,"max_turns":50}}}`. Every field but `scope.out`, `max_turns` and `read_only` (true when the piece writes nothing) is required; state `constraints` even when it is `[]`. The Wright knows only its brief.
- `inspect_wright` shows a piece and its dispatches: `{"piece":"<name>"}`. Poll it until the latest dispatch is no longer queued or running.
- `rework_wright` sends a rejected piece back: `{"piece":"<name>","reason":"<what was wrong>","diagnosis":"...","brief":{<the amended brief, complete>}}`.
- `accept_piece` accepts a piece you have checked: `{"piece":"<name>"}`.
- `handback` ends the step, once no Wright is still running: `{"status":"done|partial|failed","results":["<pointers to the accepted results>"],"checked":["<what you inspected>"],"rejected":["<what you rejected, and why>"],"uncertainty":"...","flags":[{"kind":"follow-up|decision-needed|risk","description":"...","evidence":"..."}]}`. `checked` is always required; `results` is required for done and partial; `uncertainty` or `rejected` for failed. Always finish with a handback.
