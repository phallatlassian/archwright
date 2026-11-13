# The Archwright is a stock interactive agent session, not a custom chat program

The Archwright is an ordinary interactive `claude` or `codex` session launched by the `archwright` CLI, with factory tools called through the `archwright tool` CLI (ADR 0014; an MCP shim only where policy allows) and a hook-based inbox for dashboard commands. We chose this over a Works-owned conversation (headless/SDK plus our own chat front-end) because it keeps the developer's permissions exactly those of running the agent from a terminal, under the company managed permission model, and costs the least to build. The price: dashboard commands reach the Archwright only at turn boundaries, so anything that must act instantly (such as cancelling a running Job) has to be done by the Works itself and reported to the Archwright.

There is one Archwright for the whole factory, not one per Job or repository. Which runner backs it is configured per install.
