# Angle B — removed behavior

For every deleted or replaced line, name the invariant it enforced — a guard,
a validation, a cleanup, a default, an ordering constraint, a permission check.
If that invariant is **not re-established** in the new code, it is a candidate.
Deletions are the highest-signal, lowest-visibility source of regressions:
reviewers read what was added, not what silently vanished.
