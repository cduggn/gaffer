# Decompose

The deliverable is a plan expressed as beads, not code.

1. Read enough of the code to find the seams: files, services, or layers that can change independently.
2. Split the work into child beads that each land on their own and end in a check. Order the riskiest unknown first. Put scaffolding (types, test harness, CI) before features.
3. Create each child with `bd create` using `--parent <id>`, a one-sentence outcome, and acceptance criteria tagged `[verify: <id>]` or `[review]`. Run `gaffer lint <child>` on each.
4. Express ordering with `bd dep add <later> <earlier>`. Two children that write the same files must be ordered or merged.
5. Hand off for review. The reviewer checks that every child is independently verifiable.

Report: the child list in dependency order, and what you chose not to split and why.
