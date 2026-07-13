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
    types: [opened, synchronize, reopened]

# Cancel a superseded (billed) review when a new commit is pushed.
concurrency:
  group: swatter-${{ github.event.pull_request.number }}
  cancel-in-progress: true

permissions:
  contents: read       # read the checkout to diff
  pull-requests: write # post comments
  checks: write        # post the check run

jobs:
  review:
    # Same-repo PRs only. On a public repo a fork PR gets a read-only token and
    # no secrets, so auto-review can't post — gate it out here rather than spin
    # up a runner that exits neutral. See "Fork PRs" below to review them on
    # demand.
    if: github.event.pull_request.head.repo.full_name == github.repository
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

Learning runs on a **schedule**, not per merge. A single daily job scans every
PR merged in a lookback window and folds the feedback humans left on Swatter's
inline comments — 👍/👎 reactions, replies like "good catch" or "false
positive", resolved threads, and whether the flagged line was changed before
merge — into the rule book. One scheduled job is the **sole writer** of
`.swatter/{rules,pending}.md`, so concurrent merges can never race on the file.

`.github/workflows/swatter-learn.yml`:

```yaml
name: swatter-learn
on:
  schedule:
    - cron: '0 0 * * *'   # 00:00 UTC daily
  workflow_dispatch:
    inputs:
      since:
        description: Lookback window (Go duration, e.g. 72h)
        default: '72h'

# One learn run at a time preserves the single-writer invariant.
concurrency:
  group: swatter-learn
  cancel-in-progress: false

permissions:
  contents: write      # commit the rule book to the base branch
  pull-requests: read  # read merged-PR comments + reactions

jobs:
  learn:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: lohi-ai/swatter@v1
        with:
          mode: learn
          learn_since: ${{ github.event.inputs.since || '72h' }}
          api_key: ${{ secrets.SWATTER_API_KEY }}
          model: claude-opus-4-8
```

The window (`72h`) deliberately overlaps the daily cadence: per-PR scoring is
idempotent, so a skipped or failed run self-heals on the next pass and
late-arriving reactions still get counted — a PR is never double-scored. Each
run:

- scores the rule book: confirmed-useful findings are **hits**, findings
  humans rejected are **misses** (noisy rules decay fast);
- records evidence in `.swatter/pending.md`: valuable findings no rule
  produced, and bugs *other* reviewers caught that Swatter missed;
- promotes a pattern into `.swatter/rules.md` only when its evidence reaches
  weight ≥ `rule_promote_after` (default 3; a missed bug weighs 2, a repeat 1)
  **across at least 2 distinct PRs**;
- commits both files to the base branch via the Contents API (sha
  compare-and-swap) with `[skip ci]`. Set `rules_commit: 'false'` to compute
  without committing.

Backfill one merged PR from a checkout: `swatter learn --pr 42`. Scan a window
directly: `swatter learn --since 72h`.

If you also want a per-merge *preview* (computed, never committed), add `closed`
to the review workflow's `pull_request` types — a merged PR then logs the
would-be rule-book change without writing it; the scheduled job remains the only
writer.

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

## Effort levels

`effort` selects the review level (the reference level table). Each level also
hard-caps every role agent's tokens — `high` keeps each agent under 120K, and
lower levels under that:

| level | pipeline |
|---|---|
| `low` | 1 diff pass → no verify → ≤4 findings |
| `medium` | 3+5 angles × 6 candidates → 1-vote verify → ≤8 findings (precision) |
| `high` (default) | 3+5 angles × 6 candidates → 1-vote verify (recall-biased) → ≤10 findings |
| `xhigh` | 5+5 angles × 8 candidates → 1-vote verify → sweep → ≤15 findings |
| `max` | same as xhigh; only the API reasoning effort differs, not the fan-out |

```yaml
      - uses: lohi-ai/swatter@v1
        with:
          api_key: ${{ secrets.SWATTER_API_KEY }}
          effort: low        # cheapest level; xhigh/max for the deepest pass
          max_usd: '1'       # the review-wide ledger still backstops everything
```

## `@swatter review` re-trigger (on-demand mode)

`swatter init --mode on-demand` generates a single workflow that reviews on PR
open, then only when a maintainer comments `@swatter review` — no per-commit
runs. Push commits freely; the review refreshes only when asked. To add the
comment trigger to an existing per-commit workflow by hand, extend it with:

```yaml
on:
  pull_request:
    types: [opened, reopened, closed]
  issue_comment:
    types: [created]

jobs:
  review:
    # Auto-run on same-repo PRs (closed still runs the learn flow), or a
    # "@swatter review" comment from a trusted commenter. Two gates matter on a
    # public repo: the head-repo check keeps fork PRs from auto-running (their
    # token is read-only, so it can't post anyway), and the author_association
    # check is essential because issue_comment runs with a *write* token even on
    # fork PRs — without it any drive-by commenter could spend your tokens (and
    # trigger a checkout of untrusted head).
    if: >-
      (github.event_name == 'pull_request' &&
       (github.event.action == 'closed' ||
        github.event.pull_request.head.repo.full_name == github.repository)) ||
      (github.event.issue.pull_request &&
       contains(github.event.comment.body, '@swatter review') &&
       contains(fromJSON('["OWNER","MEMBER","COLLABORATOR"]'), github.event.comment.author_association))
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
          # the comment payload has no PR head — check out refs/pull/N/head
          ref: ${{ github.event_name == 'issue_comment' && format('refs/pull/{0}/head', github.event.issue.number) || '' }}
      - uses: lohi-ai/swatter@v1
        with:
          api_key: ${{ secrets.SWATTER_API_KEY }}
          model: claude-opus-4-8
```

The reporter reuses the same sticky comment, so a re-trigger updates the
existing review in place instead of stacking comments. Because `issue_comment`
runs in the base-repo context with a write token, this is also the way to review
a **fork** PR on demand — the auto-run is gated to same-repo PRs (a fork's token
is read-only and carries no secrets, so it couldn't post anyway), but a
maintainer's `@swatter review` reviews it.

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

On a public repo the `pull_request` trigger gives fork PRs a **read-only** token
and no secrets, so an auto-review can't post. Gate the job to same-repo PRs so a
fork PR doesn't even spin up a runner:

```yaml
jobs:
  review:
    if: github.event.pull_request.head.repo.full_name == github.repository
```

(As a backstop, Swatter also detects a read-only token at runtime and exits
neutral — no red check — rather than failing, so an ungated workflow is still
safe, just noisier.)

To actually review a fork PR, have a maintainer trigger it with a comment —
`swatter init --mode on-demand` (or the [`@swatter review`](#swatter-review-re-trigger-on-demand-mode)
recipe). The `issue_comment` event runs in the base-repo context with a write
token, and the `author_association` gate limits it to OWNER/MEMBER/COLLABORATOR,
so a maintainer vouches for the code before Swatter runs on it.

Do **not** switch to `pull_request_target` to auto-review forks: it runs with
your secrets and a write token against attacker-controlled code — the classic
public-repo secret-exfiltration hole.
