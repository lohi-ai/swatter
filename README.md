# 🤚 Swatter

**A PR-review bugbot that swats bugs before they land** — validated findings
(low noise) and a *living* rule book, built on
[agentcore](https://github.com/lohi-ai/agentray). BYOK: bring an Anthropic key
or point at any OpenAI-compatible gateway (9router, OpenRouter, LiteLLM,
Ollama). Open source, self-hosted in your own CI — no data leaves your runner
except the model calls you configure.

## Why another reviewer?

Most AI reviewers do a **single pass** and post whatever the model says — noise
is the #1 complaint. Swatter runs a find-then-verify pipeline instead:

1. **Finders** — up to eight independent angles (line-by-line, removed-behavior,
   cross-file, security, cleanup, conventions, conformance, pattern-consistency)
   read the *real files*, not just the diff.
2. **Validators** — every CRITICAL/MAJOR candidate is re-checked by a *fresh*
   agent that never saw the finder's reasoning and must trace the actual code
   path. Rejects speculation; keeps what it can prove.
3. **A living rule book** (`.swatter/rules.md`) — confirmed findings teach
   rules; the book dedups, scores by hit/miss, and expires stale entries, so the
   bot gets sharper on *your* codebase over time
   ([how it works](docs/DESIGN-RULEBOOK.md)).

## Quickstart

```bash
# in your repo, with the GitHub CLI authenticated:
swatter init          # asks provider/model + review trigger, writes the workflow, sets the secret
```

`init` asks how you want reviews triggered:

- **per-commit** (default) — reviews on every push. Continuous, but pays for a
  full review per commit. (`swatter init --mode per-commit`)
- **on-demand** — reviews on PR open, then only when a maintainer comments
  `@swatter review`. No per-commit runs, so it spends far fewer tokens on churny
  PRs. (`swatter init --mode on-demand`)

…or add `.github/workflows/swatter.yml` by hand:

```yaml
name: swatter
on:
  pull_request:
    types: [opened, synchronize, reopened]
concurrency:
  group: swatter-${{ github.event.pull_request.number }}
  cancel-in-progress: true
permissions:
  contents: read
  pull-requests: write
  checks: write
jobs:
  review:
    # Same-repo PRs only. On public repos a fork PR gets a read-only token and
    # no secrets, so auto-review can't post — see docs/recipes.md for reviewing
    # fork PRs on demand.
    if: github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }        # full history for base...head diff
      - uses: lohi-ai/swatter@v0
        with:
          api_key: ${{ secrets.SWATTER_API_KEY }}
          model: claude-opus-4-8
```

Open a PR — Swatter posts inline comments, a summary comment, and a **Swatter**
check run. More patterns (gateways, `@swatter review` re-trigger, path filters,
advisory mode, fork-PR safety) in [docs/recipes.md](docs/recipes.md).

## Standalone CLI (try it before CI)

Want to see Swatter on your code before wiring up a workflow? Run a review
locally. Store your key once, verify the provider answers, then review your
current branch:

```bash
swatter config set api-key sk-…       # saved 0600 to ~/.config/swatter/config.json
swatter doctor                        # checks config, git, GitHub token + one cheap model call
swatter review                        # review the current branch vs the default branch → stdout
swatter review high                   # force an effort level (auto|low|medium|high|xhigh|max)
swatter review main..HEAD             # review an explicit git range (three-dot / merge-base)
swatter review low --comment 42       # review and post findings to PR #42 (needs a GitHub token)
```

- **`swatter config set|get|list|path`** manages `~/.config/swatter/config.json`
  (honors `$XDG_CONFIG_HOME`) so you don't export `SWATTER_*` by hand. Keys:
  `api-key`, `provider`, `base-url`, `model`, `model-cheap`, `effort`,
  `fail-on`, `github-token`, `resolve-token`. The file is layered **under** the
  environment — a set `SWATTER_*` var always wins — so CI (which sets the env
  and ships no file) is unaffected. `config list` redacts secrets.
- **`swatter doctor`** validates your config, checks the git context and (if a
  token is present) GitHub access, and does one tiny model round-trip so a bad
  key or gateway fails fast instead of mid-review. `--no-llm` skips the call.
- **`swatter review [effort] [--comment] [<target>]`** runs the same
  find-then-verify pipeline as CI. `<target>` is empty (current branch vs its
  merge-base with the default branch), a git ref/range, or a PR number/URL.
  Without `--comment`, findings print to stdout (`--format json` for machine
  output). `--comment` posts to the PR exactly as CI does — check out the PR
  branch first so inline comments anchor to the right commit, and set a GitHub
  token (`swatter config set github-token …` or `GITHUB_TOKEN`).

`run`/`learn`/`init` and the GitHub Action are unchanged — the CLI is a new
front door on the same engine, not a replacement.

## Configuration

| Input | Default | Notes |
|---|---|---|
| `api_key` | — (required) | BYOK key; store as a secret. |
| `provider` | `anthropic` | or `openai-compat`. |
| `base_url` | — | required for `openai-compat`. |
| `model` | `claude-opus-4-8`\* | strong tier (bug/security angles, large diffs). |
| `model_cheap` | = `model` | cheaper tier for cleanup angles on small diffs. |
| `effort` | `auto` | review level: `auto` (size from the diff: ≤3 files & ≤50 lines → `low`, ≤10 & ≤300 → `medium`, ≤25 & ≤1000 → `high`, larger → `xhigh`), `low` (1 diff pass → no verify → ≤4 findings), `medium` (3+5 angles × 6 → verify → ≤8, precision), `high` (same fan-out, recall-biased → ≤10), `xhigh` (5+5 angles × 8 → verify → sweep → ≤15), `max` (xhigh + API reasoning effort). Each level also hard-caps per-agent tokens — `high` stays under 120K per agent. |
| `fail_on` | `never` | advisory by default (green check + comments). Set `critical`/`major`/`any` to gate merges — the `Swatter` check goes red on confirmed findings. |
| `max_usd` | `5` | per-PR spend ceiling (priced models). |
| `max_tokens_total` | `8000000` | always-works ceiling for unknown-priced models. |
| `price_per_mtok_in`/`_out` | `0` | teach the ledger a custom model's price. |
| `resolve_token` | — | optional PAT (pull-requests: write) used **only** to resolve stale review threads across rounds. The default `GITHUB_TOKEN` can't (`Resource not accessible by integration`); without this, fixed findings' threads stay open (dedup still works). |

\* No default for `openai-compat` — name your gateway's model.

## Safety

Swatter runs untrusted PR content (diffs, descriptions can be attacker-supplied
on public repos). The review agents are **read-only** — no shell, no network
tools, no GitHub token. Findings are typed JSON rendered by the harness, which
holds the token and does all posting. An instruction smuggled into a PR body
can't make the bot post, exfiltrate, or run anything.

Every GitHub token is scoped and accounted for: on each run Swatter prints a
**token preflight** to the Action log naming which token does what — the
harness `GITHUB_TOKEN` (check run, comments, thread read) and, if set, the
`resolve_token` PAT (used for **nothing** but `resolveReviewThread`) — and
verifies each works, so a maintainer can see exactly how their credentials are
used rather than meeting an opaque permission error mid-review.

## Development

```bash
go build ./...
go test ./...                    # deterministic unit tests
SWATTER_LIVE_TEST=1 SWATTER_API_KEY=… SWATTER_MODEL=… \
  go test ./internal -run TestPipelineFixture   # live fixture replay
```

Swatter consumes agentcore from
[`github.com/lohi-ai/agentray`](https://github.com/lohi-ai/agentray), pinned in
`go.mod` and resolved from the module proxy — no extra setup to build. To hack on
agentcore and Swatter together, add a local
`replace github.com/lohi-ai/agentray => ../agentray` pointing at a sibling
checkout (and drop it before committing).

## License

[Apache-2.0](LICENSE).
