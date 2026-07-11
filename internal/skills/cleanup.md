# Cleanup — reuse / simplification / efficiency / altitude / conventions

The correctness angles hunt for bugs; you cover all five cleanup lenses in one
pass. Apply each lens to the changed code and surface candidates under it.

### Reuse
The angles above hunt for bugs; this one and the next two hunt for cleanup in
the changed code. Flag new code that re-implements something the codebase
already has — Grep shared/utility modules and files adjacent to the change,
and name the existing helper to call instead.

### Simplification
Flag unnecessary complexity the diff adds: redundant or derivable state,
copy-paste with slight variation, deep nesting, dead code left behind. Name
the simpler form that does the same job.

### Efficiency
Flag wasted work the diff introduces: redundant computation or repeated I/O,
independent operations run sequentially, blocking work added to startup or
hot paths. Also flag long-lived objects built from closures or captured
environments — they keep the entire enclosing scope alive for the object's
lifetime (a memory leak when that scope holds large values); prefer a
class/struct that copies only the fields it needs. Name the cheaper
alternative.

### Altitude
Check that each change is implemented at the right depth, not as a fragile
bandaid. Special cases layered on shared infrastructure are a sign the fix
isn't deep enough — prefer generalizing the underlying mechanism over adding
special cases.

### Conventions (CLAUDE.md + learned rules)
The scope pass has already scanned the repo's CLAUDE.md files and quoted the
applicable rules under **Conventions in force** in your brief — enforce those;
do not re-scan the repository for CLAUDE.md files yourself (only read one if a
quoted rule is ambiguous and you need its surrounding context). Check the diff
for clear violations of the quoted rules. Only if the brief has **no**
"Conventions in force" section (the scope pass failed) fall back to finding the
CLAUDE.md files yourself: the repo-root CLAUDE.md plus any CLAUDE.md /
CLAUDE.local.md in an ancestor directory of a changed file.
Also enforce the **learned rules** pasted in the brief — each is a pattern a
past bug in this repo taught; a diff that violates one is a candidate. Only flag
a violation when you can quote the exact rule and the exact line that breaks it
— no style preferences, no vague "spirit of the doc" inferences. In the finding,
name the CLAUDE.md path (or cite the rule id in `rule_ids`) and quote the rule
so the report can cite it. If no CLAUDE.md and no learned rule applies, return
nothing for this lens.

---

Cleanup, altitude, and conventions candidates use the same `file`/`line`/
`summary` shape; in `failure_scenario`, state the concrete cost (what is
duplicated, wasted, harder to maintain, or which CLAUDE.md rule / rule id is
broken) instead of a crash. These are **MINOR** — correctness bugs always
outrank cleanup, altitude, and conventions findings when the output cap forces
a cut.
