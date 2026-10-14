# gaffer reference

## Commands

Commands that take `[<id>]` accept a bead id or a run id. Without one, they act on the run whose worktree contains the current directory.

| Command | Effect |
| --- | --- |
| `gaffer init [--tracked]` | Initializes beads if absent. Writes `.gaffer/verifiers.json` and `.gaffer/standing.md` if absent. Without `--tracked`, beads runs in stealth mode and makes no commit. |
| `gaffer status [--json] [--all]` | Lists active runs (all runs with `--all`) and the ready queue, with each ready bead's playbook and first lint problem. |
| `gaffer next [--label L]... [--actor A] [--base REF]` | Claims the highest-priority ready bead that passes lint, creates its worktree, and prints the brief. Skips beads another actor holds. |
| `gaffer start <id> [--actor A] [--base REF]` | Same as `next` for one bead. Starting a bead you already run returns that run. |
| `gaffer brief [<id>] [--review]` | Prints the worker brief, or the reviewer brief with `--review`. |
| `gaffer lint <id>...` | Checks that each bead routes to a playbook, has parseable acceptance criteria, and names only registered verifiers. |
| `gaffer verify [<id>] [--json]` | Runs the defaults and every verifier the criteria name, in the run's worktree, and records the result. |
| `gaffer handoff [<id>]` | Moves the run to review if everything except review passes. Prints the reviewer brief. |
| `gaffer review <id> --pass\|--fail --notes N --actor A` | Records a verdict on the current tree. A failing verdict needs notes and returns the run to work. |
| `gaffer done [<id>]` | Closes the bead if the gate passes. Otherwise prints every reason. |
| `gaffer block [<id>] --reason R` | Sets the run to blocked and the bead status to `blocked`, with a comment. |
| `gaffer resume [<id>]` | Returns a blocked or in-review run to work. |
| `gaffer abandon [<id>] [--reason R]` | Ends the run and releases the claim. Keeps the worktree and branch. |
| `gaffer dispatch [flags]` | Works ready beads unattended. See [Dispatch](#dispatch). |
| `gaffer lesson "<rule>" [--global]` | Appends a rule to `.gaffer/standing.md`, or with `--global` to `~/.config/gaffer/standing.md`. Duplicate rules are ignored. |
| `gaffer playbooks [<name>]` | Lists playbooks with their routing and policy, or prints one. |
| `gaffer install-skill [--dir D]` | Writes the agent skill to `D/gaffer/SKILL.md`. The default for `D` is `~/.claude/skills`. |

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success. For `verify`, every check passed. |
| 1 | A check failed, the gate refused, lint found problems, or another error occurred. |
| 2 | Blocked. A verifier timed out, was missing, or changed the tree, or `next` found no ready work. |
| 64 | Usage error. |

## Acceptance criteria

gaffer reads the bead's acceptance field. Each list item that starts with an id is a criterion. Other lines are prose and are ignored.

```
- A1: retrying an export writes one row per record [verify: export-retry, unit]
- A2: the settings page explains the option [review]
- [ ] A3) both kinds of proof [verify: lint] [review]
```

- Ids start with a letter. They must be unique within the bead.
- `[verify: a, b]` names verifier ids from the registry.
- `[review]` requires an independent verdict.
- A criterion with no tag is an error.

## Verifier registry

`.gaffer/verifiers.json` is committed with the code it checks.

```json
{
  "defaults": ["tests"],
  "verifiers": [
    {"id": "tests", "claim": "Go tests pass", "command": ["go", "test", "./..."], "timeout_seconds": 600},
    {"id": "export-retry", "claim": "Retrying an export writes one row per record",
     "command": ["go", "test", "./export", "-run", "TestRetry"], "timeout_seconds": 120}
  ]
}
```

- `defaults` run for every bead.
- `command` is an argument array, run in the worktree root, with no shell. Wrap it in `sh -c` if you need one.
- A check passes when it exits 0. It is killed with its whole process group at `timeout_seconds`, and the timeout counts as blocked.
- Logs go to `.git/gaffer/runs/<run>/verifications/<id>/<verifier>.log`.
- If the tree's fingerprint differs before and after the run, the verification is blocked.
- Compared with the run's base commit, a removed verifier, a changed `command` or `timeout_seconds`, or a removed default forces independent review.

## Playbook routing

The first match wins. A `playbook:<name>` label overrides routing.

| Playbook | Selected by | Review required | Read-only |
| --- | --- | --- | --- |
| investigate | labels `investigate`, `question`, `spike`; type `spike` | yes | yes |
| decompose | label `decompose` | yes | yes |
| refactor | label `refactor` | yes | no |
| bugfix | type `bug` | no | no |
| feature | types `feature`, `story` | yes | no |
| chore | types `chore`, `task`, anything else | no | no |

`bd ready` results exclude epics. Work an epic through its children.

## The gate

`gaffer done` closes the bead only if all of the following hold:

1. The bead has at least one acceptance criterion.
2. The latest verification ran on the current fingerprint and the current acceptance criteria. Every check in it passed, and every default and named verifier ran.
3. The worktree is on the run's branch. Unless the playbook is read-only, there are no uncommitted changes.
4. If the run's playbook or any criterion requires review, or the registry was weakened, there is a passing verdict from an actor other than the worker. The verdict was given on the current fingerprint and criteria.
5. A read-only playbook's worktree is identical to its base commit.

The playbook is fixed when the run starts. Relabelling the bead affects only later runs.

## Dispatch

```
gaffer dispatch [--agent claude] [--reviewer codex] [--max 2] [--limit 0]
                [--retries 1] [--label L]... [--heartbeat 1m]
```

For each ready bead, up to `--max` at a time, the dispatcher does the following:

1. It claims the bead as `gaffer-<agent>-<random>` and creates the worktree.
2. It starts the agent in the worktree with the brief on stdin. `GAFFER_ACTOR`, `BEADS_ACTOR`, and `GAFFER_RUN` are set, and gaffer's own directory is first on `PATH`.
3. While the agent runs, it refreshes the beads lease every `--heartbeat`.
4. When the agent exits, it runs `gaffer verify` itself. If the gate passes, it closes the bead. If only review is missing, it starts `--reviewer` with the review brief. With no reviewer, the run waits in review for you.
5. If verification or review fails, it retries up to `--retries` times with the failure in the brief. After that, it blocks the bead. A reviewer that exits without a verdict blocks the bead.
6. After each run settles, it reads the queue again, so beads unblocked by that run are picked up.

If you press Ctrl-C, the dispatcher stops every agent, abandons their runs, and returns their beads to the queue. Worktrees and evidence stay.

Briefs and agent logs are kept as `.git/gaffer/runs/<run>/agent-<n>.brief.md` and `agent-<n>.log`, and the reviewer's as `review-<n>.*`.

## Agents

The built-in agents are:

```
claude: claude -p --permission-mode acceptEdits --allowedTools Bash,Read,Edit,Write,Glob,Grep --add-dir {repo}
codex:  codex exec --sandbox workspace-write --add-dir {repo}
```

To add or override agents, edit `~/.config/gaffer/config.json`:

```json
{"agents": {"claude-opus": {"command": ["claude", "-p", "--model", "opus", "--permission-mode", "acceptEdits", "--add-dir", "{repo}"]}}}
```

`{repo}` expands to the main checkout and `{worktree}` to the run's worktree. Agents need write access to the main checkout because git stores a worktree's commits in the shared `.git` directory, and beads keeps its database there.

## Files and environment

| Path or variable | Contents |
| --- | --- |
| `.gaffer/verifiers.json` | The verifier registry. Committed. |
| `.gaffer/standing.md` | Project standing orders. Committed. |
| `.git/gaffer/runs/<run-id>/` | `run.json`, verifications, the verdict, briefs, and agent logs. |
| `~/.gaffer/worktrees/<repo>-<hash>/<bead>/` | Run worktrees. The hash comes from the repository path. Override the root with `GAFFER_WORKTREES`. |
| `~/.config/gaffer/` | `config.json` and global `standing.md`. Override with `GAFFER_HOME` or `XDG_CONFIG_HOME`. |
| `GAFFER_ACTOR` | Your identity. Falls back to `BEADS_ACTOR`, then git `user.name`, then `$USER`. |
