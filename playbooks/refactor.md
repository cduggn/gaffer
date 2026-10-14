# Refactor

You own the contract. Structure changes, behavior does not.

1. Pin current behavior before moving anything: a characterization test, snapshot, or equivalence script registered as a verifier. Type checks and lint are not a pin.
2. Name the structure the code is missing. The reshape must delete branches, duplicated rules, or invalid states. Indirection alone does not qualify.
3. Delete dead code and one-caller wrappers first, then reshape on the smaller base.
4. Move in small steps that keep the pin green. When an API changes, migrate every caller and delete the old API in the same change. No shims.
5. If the cleanup exposes a bug or a missing feature, file it with `bd create ... --deps discovered-from:<id>` and keep going. Do not fix it here.
6. If the diff does not make the code easier to read, revert it.

Report: what structure changed, the pin, the equivalence evidence, and what got reverted.
