# Swatter recipes

Copy-paste workflows. All assume the API key is stored as a repo secret named
`SWATTER_API_KEY` (run `swatter init` to set it, or add it under
Settings → Secrets → Actions).

## Default PR review

`.github/workflows/swatter.yml`:

```yaml
name: swatter
on:
  pull_request:
    # closed (merged) runs the post-merge feedback/learn flow, not a review
    types: [opened, synchronize, reopened, closed]

# Cancel a superseded (billed) review when a new commit is pushed.
concurrency:
  group: swatter-${{ github.event.pull_request.number }}
  cancel-in-progress: true

permissions:
  contents: write      # read the checkout + commit .swatter/rules.md post-merge
  pull-requests: write # post comments
  checks: write        # post the check run

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0   # full history so `git diff base...head` works
      - id: swatter
        uses: lohi-ai/swatter@v1
        with:
          api_key: ${{ secrets.SWATTER_API_KEY }}
          model: claude-opus-4-8
```

`fetch-depth: 0` is required — Swatter diffs `origin/<base>...HEAD`, which needs
both refs present.

## Post-merge learning (the feedback loop)

With `closed` in the trigger list above, every **merged** PR runs `swatter`'s
learn flow instead of a review (GitHub has no dedicated merge event — `closed`
plus the payload's `merged: true` is the standard pattern; a close without
merge is a no-op). The flow reads the feedback humans left on Swatter's inline
comments — 👍/👎 reactions, replies like "good catch" or "false positive",
resolved threads, and whether the flagged line was changed before merge — and:

- scores the rule book: confirmed-useful findings are **hits**, findings
  humans rejected are **misses** (noisy rules decay fast);
- records evidence in `.swatter/pending.md`: valuable findings no rule
  produced, and bugs *other* reviewers caught that Swatter missed;
- promotes a pattern into `.swatter/rules.md` only when its evidence reaches
  weight ≥ `rule_promote_after` (default 3; a missed bug weighs 2, a repeat 1)
  **across at least 2 distinct PRs**;
- commits both files to the base branch via the Contents API (sha
  compare-and-swap, so concurrent merges can't clobber each other) with
  `[skip ci]`. Requires `contents: write`; set `rules_commit: 'false'` to
  compute without committing.

Backfill an already-merged PR from a checkout: `swatter learn --pr 42`.

## Bring-your-own gateway (9router / OpenRouter / LiteLLM / Ollama)

```yaml
      - uses: lohi-ai/swatter@v1
        with:
          provider: openai-compat
          base_url: https://9router.example/v1
          api_key: ${{ secrets.SWATTER_API_KEY }}
          model: qwen2.5-coder-32b            # strong tier
          model_cheap: qwen2.5-coder-7b        # small-diff cleanup tier
          # A custom gateway model agentcore can't price — teach the ledger so
          # max_usd still fires (or rely on max_tokens_total, always enforced):
          price_per_mtok_in: '0.90'
          price_per_mtok_out: '0.90'
```

## `@swatter review` re-trigger

Re-run the review on demand by commenting `@swatter review` on the PR:

```yaml
name: swatter-mention
on:
  issue_comment:
    types: [created]

permissions:
  contents: read
  pull-requests: write
  checks: write

jobs:
  review:
    # only PR comments that start with the mention
    if: >-
      github.event.issue.pull_request != null &&
      startsWith(github.event.comment.body, '@swatter review')
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
          ref: refs/pull/${{ github.event.issue.number }}/head
      - uses: lohi-ai/swatter@v1
        with:
          api_key: ${{ secrets.SWATTER_API_KEY }}
          model: claude-opus-4-8
```

The reporter reuses the same sticky comment, so a re-trigger updates the
existing review in place instead of stacking comments.

## Path-filtered review

Only review PRs touching sensitive areas:

```yaml
on:
  pull_request:
    paths: ['api/**', 'db/**', '**/migrations/**']
```

## Advisory mode (never fail the check)

```yaml
      - uses: lohi-ai/swatter@v1
        with:
          api_key: ${{ secrets.SWATTER_API_KEY }}
          model: claude-opus-4-8
          fail_on: never   # always neutral — comments only, never blocks merge
```

## Fork PRs

The default `pull_request` trigger gives fork PRs a **read-only** token and no
secrets — Swatter detects this and exits neutral (no red check) rather than
failing. Do **not** switch to `pull_request_target` to work around it: that runs
with your secrets against attacker-controlled code. If you must review fork PRs,
gate on a maintainer label and review the code first.
