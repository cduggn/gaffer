---
name: gaffer
description: "Work through a project's task queue with gaffer and beads: pick up ready work, follow the routed playbook in an isolated worktree, prove it with registered verifiers, get an independent review, and close only through the completion gate. Use when the user says 'what's next', 'pick up', 'work the queue', 'work on <bead id>', 'dispatch', 'file this as a task', or invokes /gaffer."
---

# Gaffer

gaffer is a CLI that sits between you and beads (`bd`). beads owns task status and ownership. gaffer owns the run: the worktree, the playbook, the evidence, and the gate that decides whether work is done. Do not track task state anywhere else.

Run `gaffer status` first. It shows active runs, the ready queue, and anything waiting on review.

## Pick the mode

- **Worker.** The user wants this session to do a task. Run `gaffer next` (or `gaffer start <id>`), `cd` into the worktree it prints, and follow the brief.
- **Coordinator.** The user wants a batch of tasks done in the background. Run `gaffer dispatch --max <n>`. It claims ready beads, launches one headless agent per worktree, verifies each when it exits, and leaves results in `gaffer status`. Your job is to review handed-off runs and unblock blocked ones, not to edit their code.
- **Filing.** The user describes work for later. Create a bead with acceptance criteria (format below) and run `gaffer lint <id>`.

## The worker loop

1. `gaffer next` claims the top ready bead, makes a worktree on branch `gaffer/<id>`, and prints a brief: goal, playbook steps, acceptance criteria, verify commands, upstream context, and standing orders. The brief is the contract. Re-read it with `gaffer brief <id>`.
2. Do the work in the worktree, following the playbook. Commit as you go. `gaffer done` refuses uncommitted work, because the branch `gaffer/<id>` must hold what was proven.
3. `gaffer verify` runs every required verifier against the current tree and records the result. Exit 0 pass, 1 a check failed, 2 blocked. Any later edit, or any change to the bead's acceptance criteria, makes the result stale, so commit first and verify last.
4. If the playbook or a criterion requires review, `gaffer handoff` moves the run to review and prints a review brief. Give that brief to a subagent on a different model than yours. The reviewer runs `gaffer review <id> --pass|--fail --notes "..." --actor reviewer:<model>`. You cannot review your own run.
5. `gaffer done` closes the bead only when the gate passes. When it refuses, it lists every reason. Fix the reasons. Never weaken acceptance criteria or verifiers to get through the gate. If the criteria are wrong, say so to the user.
6. Stuck on something only a human can resolve: `gaffer block --reason "<what you need>"`.

Found unrelated work along the way: `bd create "<title>" -t bug --deps discovered-from:<id>`. Do not fix it in this run.

## Acceptance criteria format

Each criterion is a list item in the bead's acceptance field with an id and its proof:

```
- A1: retrying an export writes one row per record [verify: export-retry]
- A2: the settings page explains the new option [review]
```

`[verify: <ids>]` names verifiers registered in `.gaffer/verifiers.json`. `[review]` needs an independent reviewer. A criterion with neither is rejected. Set it with `bd update <id> --acceptance "$(cat criteria.md)"`.

## Lessons

When the user corrects how you work and the correction will apply again, record it: `gaffer lesson "<one-line rule>"` for this project or `--global` for every project. Lessons are pasted into every future brief. Prefer a verifier over a lesson when the rule can be checked by a command.
