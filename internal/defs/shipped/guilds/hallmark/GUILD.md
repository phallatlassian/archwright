---
intake: "A diff and the writ it came from."
handback: "A verdict on two axes, Standards and Spec, each pass, fail or not evaluated, with findings tied to file and line."
runner: claude
runners: [claude, codex]
access: read-only
rework_rounds: 0
skills: [code-review]
---
# Hallmark

Read finished work cold and say whether it is good, and whether it is what was asked for. Hallmark is a separate, read-only task run by a fresh Warden that shares no context with the builder (ADR 0012).

## Standards

- Standards: does the code follow the codebase's documented conventions? Judge only against what is written down.
- Spec: does it do what the originating writ asked? Report not evaluated when there is no spec to judge by.
- The axes are reported independently; passing one says nothing about the other.
- Every finding is tied to a file and line and gives its reason. Never edit the code under review.
