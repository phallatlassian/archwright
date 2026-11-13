# The Works owns orchestration state in an embedded SQLite database

Jobs, plans, tasks, Wright runs, events and pending human asks live in an embedded SQLite database (WAL, per-user directory) owned by the Works, with an append-only events table feeding the dashboard. Writs, specs and ADRs stay in each Project's own tracker and repo, where the Pocock skills expect them; the Works holds references. We rejected storing everything as tracker items (otman vault): its markdown items suit work items but not high-frequency run events, and Projects use different trackers anyway.

The Archwright keeps no state of its own: plan and findings live in the Works and are read and written through its tools (ADR 0014), so a fresh Archwright session rehydrates from the Works. After a Works restart, Jobs, plans, tasks, events and pending asks survive; in-flight Wrights are marked interrupted and re-dispatched from their stored brief rather than resumed, and their worktrees are kept for inspection.
