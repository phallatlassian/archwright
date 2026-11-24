# Session lifecycle: one standing Warden per Guild per Job, Sessions stay open, Asks keep them open

A Session is a process the Works can leave running, so lifecycle follows context, not the one-shot run. This supersedes ADR 0004 (Wardens summoned per task) and amends ADR 0015, ADR 0002 and ADR 0021.

**Wardens.** A Job has one standing Warden Session per Guild. Its steps are serialised in that Session, so it keeps context across rounds; Wrights stay per piece. A Warden still dispatches every Wright through the Works tool `dispatch_wright` (ADR 0014), restated from ADR 0004, so every dispatch is recorded and authorised and the Works adds the Guild's standards to the brief. Review's independence comes from sharing no context with the builder, not from being fresh: a Review Warden never reuses a builder's Session (Hallmark never reuses Masonry's), but Hallmark reuses its own Warden for its second pass. Context growth is left to the CLI's own compaction.

**Open until the Job leaves active work.** A Session stays open after `report_result` or handback until the Job leaves active work (PR opened, finished, cancelled or failed); then idle Sessions exit, panes are removed and the Job window closes after a short retention. A Session that stops without reporting is nudged once, then raises a Needs-you item (continue or cancel); it is never failed automatically. A Wright that ends without `report_result` is recorded as failed with its last message kept.

**Rework.** A piece sent back for rework is Nudged into the same Wright Session; a fresh Session with the amended brief is the fallback.

**Asks (amends ADR 0015).** An Ask no longer ends the run `blocked`: the asker's Session stays open and the answer is Nudged in when it is idle. A fresh run with the original brief plus the accumulated Q&A record is only the fallback when the Session is gone, so an Ask never dies with a pane. Asks stay in the Needs-you inbox and never expire.

**Cancel.** Interrupt, then kill the pane after a short grace; the run is recorded cancelled and the worktree is kept. Cancelling a Warden cascades to its Wrights.

**Recovery (amends ADR 0002).** After a Works restart the Works adopts live panes by their recorded pane ids, reconciling SQLite, the pane options and the spool; unmatched panes are flagged as orphans and never killed. A Session whose pane is lost (tmux killed, reboot, crash) is resumed by id; failing that, the step is re-dispatched from its stored brief plus Q&A (ADR 0015). A pane that dies with no handback and no cancel makes the step "interrupted" with a Needs-you item: resume, restart fresh or cancel.

**Delivery.** The Works writes each brief to a file and pastes a short pointer (first spawn, later steps, rework, Ask answers); the exact brief stays in the Works' record. A human typing in a pane is allowed and never blocked; a prompt submission the Works did not send is recorded as human-touched. Nudges are gated on idle, because Enter at a prompt approves it (`docs/spec/tmux-runtime.md`).

**Step (amends ADR 0021).** A Step is carried out by the Job's standing Warden for that Guild, not a fresh one. Everything else in ADR 0021 stands.

Supersedes ADR 0004. Amends ADR 0002, ADR 0015 and ADR 0021.
