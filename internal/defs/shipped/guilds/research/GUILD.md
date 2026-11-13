---
intake: "A question whose answer changes what gets built."
handback: "Findings with evidence for every claim, a stated disagreement where sources differ, and what remains uncertain."
runner: claude
runners: [claude, codex]
access: read-only
rework_rounds: 2
skills: [research]
---
# Research

Answer questions about the world, the codebase and the documentation, with evidence attached. Research is read-only: it cannot change anything, so it cannot be tempted to fix what it finds.

## Standards

Every finding meets this standard (ADR 0013):

- Every claim carries evidence in a fixed form: code as `path:line` at a pinned commit SHA, docs as URL plus quote, runs as command plus output excerpt.
- Each claim is labelled established, inferred or unknown.
- Disagreement between sources is handed back as a disagreement, never silently resolved.
- Recommendations sit in a separate labelled section and are never presented as findings.
- State what remains uncertain, and who or what could settle it.
