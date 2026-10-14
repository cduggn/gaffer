# Why gaffer is built this way

gaffer exists to answer one question for every task an agent works on: is it actually done? The rest of the design follows from making that answer cheap to get and hard to fake.

## What came before

**poteto-mode** is a router skill from Cursor's pstack. It matches a request to one of about twenty playbooks, applies a library of engineering principles, and invokes supporting skills such as design bakeoffs and multi-model review. Its best ideas are routing, small verifiable units, a builder kept separate from its verifier, and a brief template that gives a worker everything it needs. Its cost is size. The entry skill depends on dozens of other skills, assumes Cursor's cloud agents and model roster, and keeps program state in a per-chat store. Nothing outlives the chat that created it.

**agent-blocks** took the parts of poteto-mode that matter for day-to-day task work and named five blocks: project instructions, a task, a worker skill, a verifier registry, and a dispatcher. It implemented one of them well. Its `verify.py` runs registered commands, kills timed-out process groups, and rejects missing, failed, and changed proof. Its other blocks were written as prose and a staged plan:

- **The task lived in two places.** Acceptance criteria and verifier ids sat in a separate `task.json` snapshot next to the real task record. The two could drift apart.
- **Fingerprints covered declared files only.** A file left off the `sources` list could change without making the evidence stale.
- **Nothing enforced the gate.** The runner proved checks passed. Closing the task was left to the worker's judgment, and the README said so.
- **No routing.** The worker skill said to pick "the smallest applicable workflow" but shipped none.
- **Beads, dispatch, recovery, and review were future work.**

## What gaffer changes

**beads is the only task store.** Status, ownership, dependencies, and acceptance criteria all live in the bead. gaffer reads the bead's `acceptance_criteria` field directly, so a separate task snapshot cannot drift. Claims are beads' atomic `update --claim`. In testing, exactly one of two simultaneous claimers won every time, and `scripts/e2e.sh` checks it on every run.

**Acceptance criteria name their proof.** Each criterion is a list item tagged `[verify: <ids>]` or `[review]`. A criterion with neither tag is a parse error. That one rule removes the most common way agent work goes wrong: a goal stated so loosely that any result can claim to meet it.

**The gate is a pure function, and gaffer closes beads only through it.** `work.Gate` takes the criteria, the latest verification, the review verdict, and the current tree fingerprint. It returns every reason the work is not done. `gaffer done` closes the bead only when that list is empty. Unit tests cover each reason with literal expected strings. A deliberately broken gate fails both the unit tests and the end-to-end script.

Evidence goes stale when anything it was gathered against changes. That includes the tree, and it also includes the acceptance criteria, which are digested into every verification and verdict. The gate also requires the work to be committed on the run's branch, so a closed bead always means `gaffer/<id>` holds the proven change. The run's playbook is fixed when the run starts, so relabelling the bead later cannot relax review or read-only rules.

**Fingerprints cover the whole tree.** A fingerprint is HEAD plus a digest of `git diff HEAD` and every untracked file. Any edit after verification makes the evidence stale. A verifier that edits the tree during its own run blocks the verification.

**Registry changes force review.** Verification compares the worktree's registry with the one at the run's base commit. Removing a verifier, changing its command, or dropping a default forces independent review, whatever the playbook says. Adding verifiers is always allowed. This catches the obvious way to weaken a check. It does not catch an edit to a script that a verifier runs. Reviewers should read changes to test and check scripts with that in mind.

**Review is independent by default.** The reviewer's identity must differ from the worker's. A dispatched worker runs with `GAFFER_ACTOR` set to its own identity, so a plain `gaffer review --actor someone-else` from the worker is refused. A verdict applies only to the tree and criteria it was given against. A later commit makes it stale. This is a guard against mistakes and lazy agents, not a security boundary. See the known limits.

**Runs are isolated.** Each run gets its own worktree on branch `gaffer/<id>`. Run state lives under the repository's shared `.git/gaffer/runs/<run-id>/`, one directory per run, so concurrent runs never write the same file and no worktree diff ever includes gaffer's state. Starting a bead you already hold adopts the existing run, so a crashed start can be retried.

**Playbooks are short and shipped in the binary.** Six playbooks distill poteto-mode's bug-fix, feature, refactoring, and investigation flows, plus chore and decompose. Each is about ten lines. A table in `internal/playbook` routes beads to playbooks by type and label and sets the gate policy: features, refactors, investigations, and decompositions require review. Investigations and decompositions are read-only, and the gate refuses them if the worktree changed.

**The brief is the contract.** From poteto-mode's orchestrate playbook comes the idea that a worker cannot ask questions. The brief carries the goal, the design notes, the criteria with their verifier commands, and the conclusions of finished upstream dependencies. It also carries the playbook, the finish steps, the things the worker must never do, and the standing orders.

**Lessons become structure.** `gaffer lesson` appends a one-line rule to the project or global standing orders, which every later brief includes. When a rule can be a command, it belongs in the verifier registry instead.

**The dispatcher trusts evidence, not agents.** `gaffer dispatch` never asks an agent whether it succeeded. When the agent exits, the dispatcher runs the verifiers itself, starts the reviewer if one is needed, and lets the gate decide. In the end-to-end test, an agent that prints "I fixed it, trust me" and changes nothing ends with its bead blocked. Its retry brief includes the failing check's output. A reviewer that exits without a verdict also blocks the bead. Each claimed bead keeps its beads lease alive until it settles. When a run settles, the dispatcher checks the queue again, so dependents that the run unblocked are picked up. Ctrl-C stops every agent and returns its bead to the queue.

## What gaffer leaves out

The following are deliberate omissions.

- **Merging.** A closed bead means the acceptance criteria are proven on branch `gaffer/<id>`. Landing that branch is your call or your CI's.
- **Design bakeoffs, multi-model swarms, and PR babysitting.** These are poteto-mode features. They earn their cost on large programs, not on a personal queue. If you want them, use the playbook prose as a starting point.
- **A server.** gaffer is a CLI. The dispatcher runs while you watch it and stops when the queue drains or the limit is reached.
- **Cross-machine leases.** beads leases are enforced only on the node that granted them. Run one dispatcher per repository.

## Known limits

- Agents run with your permissions. A dispatched agent has a shell, so it could unset `GAFFER_ACTOR` and record a review under a false name, or call `bd close` directly. gaffer makes those actions deliberate and visible in the beads history, but it does not prevent them. Run agents only where you would accept that.
- The fingerprint covers tracked changes and untracked files that are not ignored. Ignored files, such as build output, are left out on purpose. Changes inside a submodule's own working tree are not covered.
- Two `gaffer start` commands for the same bead by the same actor at the same moment can both create run records. The dispatcher gives every run its own actor, so this happens only by hand.
- Verification proves what the registered commands check, and nothing more. A verifier that asserts too little passes too easily. The registry's `claim` field exists so a reviewer can compare what a check claims with what it does.
- An interactive worker does not send beads heartbeats. If you leave a claimed bead idle past its lease, `bd reclaim` can return it to the queue. Your run directory and worktree stay where they are.
