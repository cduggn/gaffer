# gaffer

gaffer runs your [beads](https://github.com/gastownhall/beads) task queue through coding agents and closes a task only when there is current evidence that it is done.

You keep the backlog in beads. gaffer claims a bead and gives it an isolated git worktree. It routes the bead to a playbook (bug fix, feature, refactor, investigation, chore, decompose) and hands the agent a complete brief. The bead closes when a completion gate passes. The gate checks two things:

- Every verifier the acceptance criteria name has passed against the exact tree that exists now.
- Where review is required, an independent reviewer has passed that same tree.

An agent that says "done" without that evidence gets its task blocked, not closed.

gaffer is the working successor to [agent-blocks](https://github.com/cduggn/agent-blocks), which laid out these building blocks but implemented only the verification runner. It borrows its routing, principles, and brief format from [poteto-mode](https://github.com/cursor/plugins/tree/main/pstack/skills/poteto-mode). [Why gaffer is built this way](docs/design.md) compares the three.

## Install

You need Go 1.24 or later, git, and the `bd` CLI.

```bash
brew install beads            # or see the beads README
go install github.com/cduggn/gaffer/cmd/gaffer@latest
gaffer install-skill          # adds the gaffer skill to ~/.claude/skills
```

beads sends anonymous usage metrics by default. To turn them off, set `DO_NOT_TRACK=1` or run `bd metrics off`.

## Work your first task

In this walkthrough, you set up a repository, file a bead, and take it through the gate by hand. Dispatching agents comes after.

1. Set up gaffer in a git repository:

   ```bash
   cd ~/code/myproject
   gaffer init
   ```

   You see `wrote .gaffer/verifiers.json`. gaffer guesses a `tests` verifier from your build files. It initializes beads in local-only mode, so nothing is committed and your git hooks stay as they are. Pass `--tracked` to let beads commit its setup.

2. Open `.gaffer/verifiers.json` and make the commands match your project. Each verifier is a command that exits 0 when its claim holds. Then commit the `.gaffer/` directory.

3. File a bead whose acceptance criteria say how each outcome is proven:

   ```bash
   bd create "Retry writes one row per record" -t bug \
     --acceptance "- A1: retrying an export writes one row per record [verify: tests]"
   gaffer lint <id>
   ```

   `gaffer lint` prints `ok`. A criterion without `[verify: <id>]` or `[review]` is rejected, because nothing could ever prove it.

4. Claim the next ready bead:

   ```bash
   gaffer next
   ```

   gaffer prints the brief and the worktree path, for example `~/.gaffer/worktrees/myproject-1a2b3c/<id>`. Change to that directory.

5. Make the fix, commit it, and close the bead:

   ```bash
   gaffer verify    # exit 0 pass, 1 a check failed, 2 blocked
   gaffer done
   ```

   If you have uncommitted changes, `gaffer done` refuses. If anything changed after `gaffer verify`, it refuses and says `verification is stale`. Commit, then run `gaffer verify` again. When the gate passes, the bead closes with a comment that lists every check, and the branch `gaffer/<id>` holds the work. Merging is up to you.

## Let agents do it

Inside Claude Code, the skill lets you say "what's next" or "work the queue". The agent then runs the same loop and hands review to a subagent on a different model.

To work a batch unattended, run the dispatcher:

```bash
gaffer dispatch --agent claude --reviewer codex --max 2
```

The dispatcher starts one headless agent per bead, each in its own worktree. When an agent exits, the dispatcher runs the verifiers itself. If they pass and review is required, it starts the reviewer. The bead closes only through the gate. A run that fails verification gets one retry by default, with the failure output in its brief. After that, the dispatcher blocks the bead with the evidence path. `gaffer status` shows what happened.

Dispatched agents edit files and run shell commands without asking you. Run the dispatcher only on repositories and machines you trust them with.

## Learn more

- [Why gaffer is built this way](docs/design.md): the design, and how it compares to agent-blocks and poteto-mode.
- [Command and file reference](docs/reference.md): commands, exit codes, the registry, acceptance syntax, and agent configuration.
- `gaffer playbooks <name>` prints a playbook.
- `scripts/e2e.sh` runs every gate path against real git and real `bd`.

## Develop

```bash
make test    # gofmt, go vet, unit tests
make e2e     # end-to-end against real git and bd
```
