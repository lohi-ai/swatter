# The living rule book

The rule book is what makes Swatter get sharper on *your* codebase instead of
repeating the same generic advice every PR. It is a small, self-maintaining file
of learned review rules. This spec is shared by two consumers:

- **Swatter** — `.swatter/rules.md` in the repo under review.
- **babysit `review-pr`** — `<repo>/.babysit/review-pr.md`.

Both use the identical entry format and lifecycle so a rule learned in one is
readable by the other, and neither grows an unbounded append-only list.

## Entry format

```markdown
- id: r-2026-07-11-1
  rule: Wrap external API calls in the shared withRetry helper
  origin: PR#42 2026-07-11   confidence: 0.90
  path: api/client.go
  hits: 3   last_hit: 2026-07-10   misses: 1
```

| Field | Meaning |
|---|---|
| `id` | Stable `r-<date>-<n>` identifier a finder cites when the rule fires. |
| `rule` | One sentence, actionable, general — the *class* of mistake, never the instance. |
| `origin` | Where it was learned (PR/branch + date). |
| `path` | Optional file the rule is anchored to; if that file leaves the repo the rule expires. |
| `confidence` | 0–1. Rises on hits, falls fast on misses. |
| `hits` / `misses` | Times the rule fired on a surviving finding / produced a rejected one. |
| `last_hit` | Recency, for decay. |

## What is a rule

A rule is a generalized pattern **a future diff could violate**, distilled from
a CONFIRMED finding. "Wrap external API calls in `withRetry`" is a rule; "PR #42
forgot retry on line 88" is a one-off fact and is **not** stored. This is the
same bar review-pr's Learn section already sets — the lifecycle below is what's
new.

## Lifecycle (run once after each review, in order)

1. **Learn.** Each CONFIRMED finding → at most one candidate rule (generalized).
   No confirmed findings → nothing to learn.
2. **Dedup.** Before inserting, compare the candidate to every existing rule:
   - a normalized-text match (lowercased, punctuation-folded) is a free reject;
   - otherwise an LLM *same-pattern judge* catches paraphrase, generalization,
     and subset relationships (an exact-match-only guard silently accumulates
     near-duplicates — the lesson from the Litrans bible dedup work).
3. **Score.** Every review updates counts from that run's outcome:
   - a rule id cited by a **surviving** finding → `hits++`, `last_hit=today`,
     confidence rises toward 1 (`c += (1-c)·0.1`);
   - a rule id cited by a **rejected** candidate → `misses++`, confidence falls
     fast (`c ·= 0.7`). Rules that generate noise decay quickest.
4. **Compact / expire.** When the rendered book exceeds **4 KB** *or* every
   **20 reviews**:
   - a rule whose `path` no longer exists is expired immediately;
   - remaining rules are ranked by `confidence × recency-decay` (≈60-day
     half-life); sub-floor, never-hit rules are dropped, then the lowest-ranked
     are trimmed until the book fits 4 KB.

The whole book (≤4 KB) is pasted verbatim into every finder brief, so enforcing
learned rules costs no extra tokens per finding and the cost stays flat as the
book turns over rather than grows.

## Write-back

Committing a rules file from CI races concurrent PRs on one path and can loop.
Default is **suggestion mode**: the updated book is computed and offered (e.g. a
PR comment / suggestion) for a human to merge. Direct commit is opt-in
(`SWATTER_RULES_WRITE=1` for Swatter) and must use fetch-rebase-retry plus a
`[skip ci]` marker. review-pr writes to `.babysit/review-pr.md` in the working
tree only (never commits) — the babysit ticket flow owns the commit.

## Reference implementation

`swatter/internal/rules.go` (deterministic core: parse/render, score,
compact/expire) and `swatter/internal/rules_llm.go` (learn + dedup judge). Unit
tests in `rules_test.go` cover round-trip, paraphrase dedup, scoring, path-gone
expiry, and score-ranked compaction.
