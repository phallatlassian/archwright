# Putting an ask to the developer (Archwright procedure, ADR 0015)

For each open ask held by the Archwright (`archwright tool list_asks` with `{"status":"open","holder":"archwright"}`, or the Needs-you inbox), at your next turn boundary:

1. `archwright ask page ASK_ID`. If `mode` is `lavish`, tell the developer the page is open and run `lavish-axi poll <file>`. If `mode` is `text`, put the returned `text` in the session and wait for the developer's reply.
2. On a Lavish submit, pipe the prompt's `data` to `archwright ask record ASK_ID`. It records the answer through the tool API (`answer_ask`, `accept_default` or `skip_ask`) and ends the page. If it reports `recorded: false`, the dashboard answered first: tell the developer, nothing else to do.
3. For a plain-text reply, call `archwright tool answer_ask` (answers: `{"question_id","values"}` or `{"question_id","skipped":true}`), `accept_default` or `skip_ask`, then `archwright ask end ASK_ID`.
4. Call `accept_default`, `skip_ask` or `cancel_ask` only when the developer asks for it. Never type a secret into an ask; manual steps are a checklist the developer confirms.

Limit: only Wright tasks park and re-dispatch after an answer. A Warden's blocking ask does not start a fresh run; the Warden ends its run and reads the answer with `show_ask` / `list_asks` when next summoned.
