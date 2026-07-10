# Angle A — line-by-line

Go through every hunk, every changed line. For each: what input, state,
timing, or platform makes it wrong? Off-by-one, nil/undefined deref, wrong
operator, unhandled error path, type coercion, boundary and empty-collection
cases, integer/precision overflow, timezone/locale, concurrency races on
shared state. Read the enclosing function for each hunk — a changed line can
break an invariant an unchanged line downstream relies on.
