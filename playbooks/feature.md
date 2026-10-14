# Feature

You own the design. Name the data shape before writing logic.

1. Read the code the feature touches until you can state its current contract in two sentences.
2. Name the core types and the structure that organizes them: a state machine over scattered booleans, a table over a growing if/else chain, a typed model over repeated shape assumptions. Write that choice in the bead's design field with `bd update <id> --design`.
3. If two designs are plausible and the choice is hard to reverse, sketch both briefly and pick from evidence, not taste.
4. Build scaffold before behavior: types, then the test harness, then the logic.
5. Keep validation at system boundaries and business logic pure. Do not add layers that have a single caller.
6. Commit in small units that each pass `gaffer verify`.
7. Hand off for independent review with `gaffer handoff`. A reviewer who is not you runs `gaffer review`.

Report: what you built, the data shape and why, alternatives you rejected, open decisions.
