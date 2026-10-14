# Bug fix

You own the fix. Every shipped line traces to runtime evidence.

1. Reproduce the defect yourself on the surface where it was reported. If it will not reproduce, tighten conditions or add instrumentation until it does. Record the repro command in the bead with `bd comments add`.
2. Write the repro as a failing check first. Register it in `.gaffer/verifiers.json` if no existing verifier covers it, and reference it from the acceptance criteria.
3. Find the root cause. List candidate hypotheses, then eliminate them with runtime evidence, cutting the largest remaining space each pass. Ask why until the mechanism is confirmed, not just correlated.
4. Make the smallest change the evidence justifies. A change that "might help" is a hypothesis, not a fix, and does not ship.
5. Commit the failing check before the fix so history shows red then green.
6. Run `gaffer verify`. The original repro must now pass alongside the project defaults.

Report: what was broken, the root cause, the fix, and the repro output before and after.
