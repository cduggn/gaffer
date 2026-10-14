#!/usr/bin/env bash
# End-to-end proof against real git and real bd. Every gate path that matters
# is exercised: refusal without evidence, failed and stale verification,
# claim races, independent review, weakened verifiers, read-only playbooks,
# and unattended dispatch with a scripted agent.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
export DO_NOT_TRACK=1
export GAFFER_HOME="$WORK/home" GAFFER_WORKTREES="$WORK/worktrees"
export GIT_AUTHOR_NAME=e2e GIT_AUTHOR_EMAIL=e2e@example.com GIT_COMMITTER_NAME=e2e GIT_COMMITTER_EMAIL=e2e@example.com
unset GAFFER_ACTOR BEADS_ACTOR BD_ACTOR

go build -o "$WORK/bin/gaffer" "$ROOT/cmd/gaffer"
export PATH="$WORK/bin:$PATH"

pass=0
ok() { pass=$((pass + 1)); printf 'ok %2d  %s\n' "$pass" "$1"; }
die() { printf 'FAIL  %s\n' "$1" >&2; exit 1; }
# expect <code> <label> <command...>: run, require the exit code, keep output in $OUT.
expect() {
  local want=$1 label=$2; shift 2
  set +e; OUT=$("$@" 2>&1); local got=$?; set -e
  [ "$got" = "$want" ] || { printf '%s\n' "$OUT" >&2; die "$label: exit $got, want $want"; }
  ok "$label"
}
contains() { grep -qF -- "$2" <<<"$OUT" || { printf '%s\n' "$OUT" >&2; die "$1: missing \"$2\""; }; ok "$1"; }
bead() { bd -C "$REPO" "$@" --json; }
new() { bead create "$@" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])'; }
status_of() { bead show "$1" | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["status"])'; }
wt() { echo "$GAFFER_WORKTREES"/repo-*/"$1"; }

REPO="$WORK/repo"
mkdir -p "$REPO" && cd "$REPO"
git init -q -b main
echo 41 > value.txt
git add value.txt && git commit -qm init

expect 0 "init sets up beads and a starter registry" gaffer init
contains "init leaves git history alone" "ready:"
[ "$(git rev-list --count HEAD)" = 1 ] || die "bd init committed to the repo"
[ -z "$(git config --get core.hooksPath || true)" ] || die "bd init took over core.hooksPath"
cat > .gaffer/verifiers.json <<'EOF'
{
  "defaults": ["sane"],
  "verifiers": [
    {"id": "sane", "claim": "value.txt exists", "command": ["test", "-f", "value.txt"], "timeout_seconds": 10},
    {"id": "answer", "claim": "value.txt holds 42", "command": ["sh", "-c", "test \"$(cat value.txt)\" = 42"], "timeout_seconds": 10}
  ]
}
EOF
git add .gaffer && git commit -qm "register verifiers"

# Linting and refusal to start unverifiable work.
VAGUE=$(new "make it better" -t task -p 0)
expect 1 "lint rejects a bead without acceptance criteria" gaffer lint "$VAGUE"
contains "lint names the missing criteria" "no acceptance criteria"
expect 1 "start refuses an unverifiable bead" gaffer start "$VAGUE" --actor w1
bd -C "$REPO" close "$VAGUE" --reason "out of scope for e2e" -q

UNREG=$(new "uses an unknown check" -t task --acceptance "- A1: works [verify: nope]")
expect 1 "lint catches an unregistered verifier" gaffer lint "$UNREG"
contains "lint names the verifier" "verifier nope is not registered"
bd -C "$REPO" close "$UNREG" --reason "fixture" -q

# The bug-fix path: no evidence, failing evidence, stale evidence, then done.
BUG=$(new "value should be 42" -t bug -p 1 --acceptance "- A1: value.txt holds 42 [verify: answer]")
expect 0 "next claims the top ready bead" gaffer next --actor w1
contains "brief routes a bug to the bugfix playbook" "## Playbook: bugfix"
contains "brief lists the verifier command" 'answer: `sh -c test "$(cat value.txt)" = 42`'
[ "$(status_of "$BUG")" = in_progress ] || die "bead not claimed"
ok "bead is in_progress in beads"
cd "$(wt "$BUG")"

expect 1 "done refuses with no verification" gaffer done
contains "done explains why" "no verification has run"
expect 1 "verify fails on the unfixed tree" gaffer verify
contains "verify reports the failing check" "fail   answer"
echo 42 > value.txt
expect 0 "verify passes after the fix" gaffer verify
expect 1 "done refuses uncommitted work" gaffer done
contains "the branch must hold the work" "uncommitted changes"
echo "# trailing edit" >> value.txt
expect 1 "done refuses stale evidence" gaffer done
contains "stale evidence is named" "verification is stale"
echo 42 > value.txt && git commit -qam "fix value"
expect 0 "verify passes on the committed fix" gaffer verify
expect 0 "done closes the bead" gaffer done
[ "$(status_of "$BUG")" = closed ] || die "bead not closed"
ok "bead is closed in beads"
cd "$REPO"

# Claim race: two actors start the same bead at once; exactly one wins.
RACE=$(new "race me" -t task --acceptance "- A1: value exists [verify: sane]")
set +e
gaffer start "$RACE" --actor ra >"$WORK/ra" 2>&1 & A=$!
gaffer start "$RACE" --actor rb >"$WORK/rb" 2>&1 & B=$!
wait $A; RA=$?; wait $B; RB=$?
set -e
[ $((RA == 0)) -ne $((RB == 0)) ] || { cat "$WORK/ra" "$WORK/rb"; die "race: exits $RA and $RB, want exactly one winner"; }
ok "claim race has exactly one winner"
WINNER=ra; [ $RA = 0 ] || WINNER=rb
expect 0 "the winner can abandon its run" gaffer abandon "$RACE" --reason "e2e cleanup"
[ "$(status_of "$RACE")" = open ] || die "abandon did not release"
ok "abandon returns the bead to the queue"
bd -C "$REPO" close "$RACE" --reason fixture -q

# Review path: the worker cannot approve its own run; a reviewer can.
FEAT=$(new "document the answer" -t feature --acceptance "- A1: value is 42 [verify: answer]
- A2: README explains why [review]")
expect 0 "start a feature" gaffer start "$FEAT" --actor w2
cd "$(wt "$FEAT")"
echo 42 > value.txt && echo "42 is the answer" > README && git add -A && git commit -qm feature
expect 0 "feature verifies" gaffer verify
expect 1 "done requires review" gaffer done
contains "gate asks for an independent reviewer" "review required"
expect 0 "handoff prints a review brief" gaffer handoff
contains "review brief tells the reviewer how to record a verdict" "gaffer review $FEAT --pass"
expect 1 "self-review is refused" gaffer review "$FEAT" --pass --notes lgtm --actor w2
expect 1 "a worker cannot review itself under another name" env GAFFER_ACTOR=w2 gaffer review "$FEAT" --pass --notes lgtm --actor reviewer:fake
expect 1 "a failing review needs notes" gaffer review "$FEAT" --fail --actor reviewer:other
expect 0 "a different actor fails the review" gaffer review "$FEAT" --fail --notes "README lacks detail" --actor reviewer:other
expect 1 "done refuses after a failed review" gaffer done
contains "the failed review is the reason" "review failed: README lacks detail"
echo "42 is the answer to everything" > README && git commit -qam "more detail"
expect 0 "verify again" gaffer verify
expect 0 "re-handoff" gaffer handoff
expect 0 "reviewer passes" gaffer review "$FEAT" --pass --notes "checked README and value" --actor reviewer:other
expect 0 "done closes a reviewed feature" gaffer done
cd "$REPO"

# Weakening a verifier forces review even for a review-free playbook.
WEAK=$(new "sneaky" -t bug --acceptance "- A1: value is 42 [verify: answer]")
expect 0 "start a bug" gaffer start "$WEAK" --actor w3
cd "$(wt "$WEAK")"
python3 - <<'EOF'
import json
p = ".gaffer/verifiers.json"
r = json.load(open(p))
for v in r["verifiers"]:
    if v["id"] == "answer":
        v["command"] = ["true"]
json.dump(r, open(p, "w"), indent=2)
EOF
git commit -qam "loosen the check"
expect 0 "the loosened check passes" gaffer verify
contains "verify flags the weakened verifier" "answer changed"
expect 1 "done refuses without review" gaffer done
contains "the gate names the change" "verifiers changed since base (answer changed)"
expect 0 "abandon the sneaky run" gaffer abandon --reason "e2e: weakened verifier"
cd "$REPO"
bd -C "$REPO" close "$WEAK" --reason fixture -q

# Read-only playbooks may not change files.
Q=$(new "why is it 41?" -t task -l investigate --acceptance "- A1: answer posted [review]")
expect 0 "start an investigation" gaffer start "$Q" --actor w4
contains "investigations route to the read-only playbook" "## Playbook: investigate"
cd "$(wt "$Q")"
echo 0 > value.txt && git commit -qam "oops"
expect 0 "verify (defaults only)" gaffer verify
expect 0 "review by another actor" gaffer review "$Q" --pass --notes ok --actor reviewer:x
bd -C "$REPO" update "$Q" --add-label playbook:chore -q
expect 1 "done refuses a read-only run that changed files, even after relabelling" gaffer done
contains "the gate says why" "read-only but the worktree differs"
expect 0 "abandon it" gaffer abandon --reason "e2e"
cd "$REPO"
bd -C "$REPO" close "$Q" --reason fixture -q

# Lessons reach every future brief.
expect 0 "record a lesson" gaffer lesson "Always name the data shape first."
git add .gaffer && git commit -qm lesson

# Unattended dispatch with scripted agents: one fixes its bead, one never does.
mkdir -p "$GAFFER_HOME"
cat > "$WORK/bin/good-agent" <<'EOF'
#!/bin/sh
brief=$(cat)
case "$brief" in *"Always name the data shape first."*) ;; *) echo "lesson missing from brief"; exit 1;; esac
echo 42 > value.txt && git commit -qam "agent fix"
EOF
cat > "$WORK/bin/lazy-agent" <<'EOF'
#!/bin/sh
cat >/dev/null
echo "I fixed it, trust me"
EOF
chmod +x "$WORK/bin/good-agent" "$WORK/bin/lazy-agent"
cat > "$GAFFER_HOME/config.json" <<EOF
{"agents": {"good": {"command": ["$WORK/bin/good-agent"]}, "lazy": {"command": ["$WORK/bin/lazy-agent"]}}}
EOF

D1=$(new "dispatch me" -t bug -l batch1 --acceptance "- A1: value is 42 [verify: answer]")
expect 0 "dispatch runs a scripted agent to completion" gaffer dispatch --agent good --label batch1 --max 2
contains "dispatch reports done" "$D1: done"
[ "$(status_of "$D1")" = closed ] || die "dispatched bead not closed"
ok "dispatched bead is closed only after independent verification"

D2=$(new "lazy agent" -t bug -l batch2 --acceptance "- A1: value is 42 [verify: answer]")
expect 0 "dispatch with an agent that claims success but changes nothing" gaffer dispatch --agent lazy --label batch2 --retries 1
contains "dispatcher retries once" "retrying (attempt 2)"
[ "$(status_of "$D2")" = blocked ] || die "lazy bead should be blocked, is $(status_of "$D2")"
ok "a self-reported success without evidence ends blocked, not closed"
grep -q "Previous attempt" "$(ls -d "$REPO"/.git/gaffer/runs/*/ | tail -1)"agent-2.brief.md \
  || die "retry brief lacks the failure"
ok "the retry brief carries the previous failure"

# Dependencies unblocked mid-dispatch are picked up in the same dispatch.
DA=$(new "first" -t bug -l batch3 --acceptance "- A1: value is 42 [verify: answer]")
DB=$(new "second" -t bug -l batch3 --deps "$DA" --acceptance "- A1: value is 42 [verify: answer]")
expect 0 "dispatch drains a dependency chain" gaffer dispatch --agent good --label batch3 --max 2
[ "$(status_of "$DA")" = closed ] && [ "$(status_of "$DB")" = closed ] || die "chain not drained: $(status_of "$DA") $(status_of "$DB")"
ok "a bead unblocked mid-dispatch is worked in the same dispatch"

# Scripted reviewers: one approves, one exits without a verdict.
cat > "$WORK/bin/approver" <<'EOF'
#!/bin/sh
id=$(sed -n 's/^gaffer review \([^ ]*\) --pass.*/\1/p' | head -1)
gaffer review "$id" --pass --notes "scripted approval" --actor reviewer:approver
EOF
printf '#!/bin/sh\ncat >/dev/null\n' > "$WORK/bin/silent"
printf '#!/bin/sh\ncat >/dev/null\nsleep 60\n' > "$WORK/bin/sleeper"
chmod +x "$WORK/bin/approver" "$WORK/bin/silent" "$WORK/bin/sleeper"
cat > "$GAFFER_HOME/config.json" <<EOF
{"agents": {"good": {"command": ["$WORK/bin/good-agent"]}, "lazy": {"command": ["$WORK/bin/lazy-agent"]},
 "approver": {"command": ["$WORK/bin/approver"]}, "silent": {"command": ["$WORK/bin/silent"]},
 "sleeper": {"command": ["$WORK/bin/sleeper"]}}}
EOF
R1=$(new "reviewed dispatch" -t feature -l batch4 --acceptance "- A1: value is 42 [verify: answer]")
expect 0 "dispatch with a reviewer agent" gaffer dispatch --agent good --reviewer approver --label batch4
contains "the reviewer agent's verdict closes the bead" "$R1: done (reviewed)"
[ "$(status_of "$R1")" = closed ] || die "reviewed dispatch not closed"
ok "reviewed dispatch closes the bead"

R2=$(new "unreviewed dispatch" -t feature -l batch5 --acceptance "- A1: value is 42 [verify: answer]")
expect 0 "dispatch with a reviewer that never decides" gaffer dispatch --agent good --reviewer silent --label batch5
[ "$(status_of "$R2")" = blocked ] || die "silent review should block, got $(status_of "$R2")"
ok "a reviewer that exits without a verdict blocks the bead"

# Ctrl-C stops agents and returns their beads to the queue.
S1=$(new "interrupt me" -t bug -l batch6 --acceptance "- A1: value is 42 [verify: answer]")
gaffer dispatch --agent sleeper --label batch6 >"$WORK/int.log" 2>&1 &
DPID=$!
for _ in $(seq 50); do grep -q "started run" "$WORK/int.log" && break; sleep 0.2; done
kill -INT $DPID
set +e; wait $DPID; set -e
grep -q "interrupted; bead released" "$WORK/int.log" || { cat "$WORK/int.log"; die "interrupt not handled"; }
[ "$(status_of "$S1")" = open ] || die "interrupted bead should be open, got $(status_of "$S1")"
if pgrep -f "$WORK/bin/sleeper" >/dev/null; then die "agent survived the interrupt"; fi
ok "interrupting dispatch kills agents and releases beads"

expect 0 "status renders" gaffer status
contains "status shows the blocked run" "blocked"

printf '\n%d checks passed\n' "$pass"
