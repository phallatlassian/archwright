# Design It Twice

When asked to explore alternative interfaces for a chosen deepening candidate, use this pattern. You work alone: there is no user to show anything to and no sub-agent to hand a design to, so you do every step yourself and put what you find in your result. Based on "Design It Twice" (Ousterhout): your first idea is unlikely to be the best.

Uses the vocabulary in [SKILL.md](SKILL.md): **module**, **interface**, **seam**, **adapter**, **leverage**.

## Process

### 1. Frame the problem space

Before designing, write down the problem space for the chosen candidate:

- The constraints any new interface would need to satisfy
- The dependencies it would rely on, and which category they fall into (see [DEEPENING.md](DEEPENING.md))
- A rough illustrative code sketch to ground the constraints, not a proposal, just a way to make the constraints concrete

Keep it for your result, then proceed to Step 2.

### 2. Design 3+ alternatives

Produce 3+ designs one after another. Each must be a **radically different** interface for the deepened module. Start each from the technical facts alone (file paths, coupling details, dependency category from [DEEPENING.md](DEEPENING.md), what sits behind the seam), not from the design before it. Give each design a different constraint:

- Design 1: "Minimize the interface: aim for 1–3 entry points max. Maximise leverage per entry point."
- Design 2: "Maximise flexibility: support many use cases and extension."
- Design 3: "Optimise for the most common caller: make the default case trivial."
- Design 4 (if applicable): "Design around ports & adapters for cross-seam dependencies."

Use both [SKILL.md](SKILL.md) vocabulary and GLOSSARY.md vocabulary so every design names things consistently with the architecture language and the project's domain language.

Each design gives:

1. Interface (types, methods, params, plus invariants, ordering, error modes)
2. Usage example showing how callers use it
3. What the implementation hides behind the seam
4. Dependency strategy and adapters (see [DEEPENING.md](DEEPENING.md))
5. Trade-offs: where leverage is high, where it's thin

### 3. Present and compare

Set the designs out one after another in your result, then compare them in prose. Contrast by **depth** (leverage at the interface), **locality** (where change concentrates), and **seam placement**.

After comparing, give your own recommendation: which design you think is strongest and why. If elements from different designs would combine well, propose a hybrid. Be opinionated: the reader wants a strong read, not a menu.
