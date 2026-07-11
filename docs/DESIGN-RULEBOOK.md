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

## Human feedback (post-merge learn flow)

The lifecycle above scores rules with Swatter's own validator. The learn flow
adds the human signal from **merged PRs**. It runs on a **daily schedule**
(`swatter learn --since 72h`), not per merge: a single scheduled job scans every
PR merged in the lookback window, which makes it the **sole writer** of the rule
book so concurrent merges never race on the file. The window overlaps the daily
cadence, and per-PR scoring is idempotent (`RuleStore.HasScored`), so a missed
run self-heals and no PR is ever double-scored. (A merged `pull_request`
`closed` event can still run the flow in *compute-only* mode for a per-merge
preview — it never commits; the schedule is the only writer.) Per PR:

1. **Read-back.** Every inline comment Swatter posts embeds an invisible
   marker (`<!-- swatter:finding {"rule_ids":[…],"summary":…} -->`). Swatter
   lists the PR's review comments (+ reactions), resolves
   thread state via one GraphQL call, and classifies each of its threads:
   - *explicit*: 👍/👎 reactions and replies ("fixed", "good catch" vs
     "false positive", "not a bug"); net positive → **hit**, net negative →
     **miss** for the finding's `rule_ids` (fed to the same Score step);
   - *implicit* (tie-breaker only): a resolved thread or an outdated anchor
     (the flagged line changed before merge) counts as a weak hit;
   - silence is never a signal.
2. **Gap evidence.** Two comment classes become *observations* in
   `.swatter/pending.md` instead of rules:
   - a positively-received Swatter finding **no rule produced** (`repeat`,
     weight 1);
   - a bug **another reviewer** (human or other bot) caught that Swatter
     missed, when it was acted on — line changed, thread resolved, or an
     affirming reply (`missed`, weight 2).
   Observations age out after 120 days and the ledger is capped at 60 entries.
3. **Conservative promotion.** One clustering pass groups same-pattern
   observations (and discards nits/questions/chatter). A cluster becomes a
   rule only when its harness-verified evidence reaches weight ≥ 3
   (`SWATTER_RULE_PROMOTE_AFTER`) across **≥ 2 distinct PRs** — one noisy PR
   can never mint a rule. Promoted rules start at confidence 0.7 (below
   validator-learned rules) and pass the same dedup judge; spent observations
   leave the ledger.

## Write-back

**During a review** committing races concurrent PRs on one path, so the
in-review lifecycle stays suggestion-mode by default (`SWATTER_RULES_WRITE=1`
to force a working-tree write). **The scheduled learn job** is the safe write
point — as the sole writer it can commit without contending: it commits
`.swatter/{rules,pending}.md` to the base branch through
the GitHub **Contents API**, where every write carries the blob sha it read —
a compare-and-swap. Two merges racing on the file leave the loser with a
conflict; it refetches, re-applies its deltas onto the fresh content, and
retries (≤3). Commit messages carry `[skip ci]` so the write never triggers
another run. Opt out with `rules_commit: 'false'`. review-pr writes to
`.babysit/review-pr.md` in the working tree only (never commits) — the babysit
ticket flow owns the commit.

## Reference implementation

`swatter/internal/rules.go` (deterministic core: parse/render, score,
compact/expire), `swatter/internal/rules_llm.go` (learn + dedup judge),
`swatter/internal/feedback.go` (marker + feedback classification),
`swatter/internal/observations.go` (pending ledger + promotion evidence),
`swatter/internal/feedback_llm.go` (clustering/promotion),
`swatter/internal/committer.go` + `learn.go` (CAS write-back + orchestration).
Unit tests in `rules_test.go`, `feedback_test.go`, `observations_test.go`, and
`committer_test.go` cover round-trip, paraphrase dedup, scoring, path-gone
expiry, score-ranked compaction, feedback classification, promotion
thresholds, and conflict-retry commits.
