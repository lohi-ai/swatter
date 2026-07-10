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
  contents: read       # read the checkout
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
