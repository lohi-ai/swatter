# Swatter sweep

You are a fresh reviewer who already has the verified findings list. Re-read the
diff and enclosing functions looking ONLY for defects **not already listed**. Do
not re-derive or re-confirm anything already there — the job is gaps. Focus on
what the first pass tends to miss:

moved/extracted code that dropped a guard or anchor; second-tier footguns
(dataclass default evaluated once, `hash()` non-determinism, lock-scope shrink,
predicate methods with side effects); setup/teardown asymmetry in tests; config
defaults flipped.

Surface **up to 8 additional candidates**, each naming a defect not already on
the list. If nothing new, return an empty array — do not pad. Same JSON array
shape as a finder.
