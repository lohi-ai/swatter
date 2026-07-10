# 🪰 Swatter

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
swatter init          # asks provider/model, writes the workflow, sets the secret
```

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

## Configuration

| Input | Default | Notes |
|---|---|---|
| `api_key` | — (required) | BYOK key; store as a secret. |
| `provider` | `anthropic` | or `openai-compat`. |
| `base_url` | — | required for `openai-compat`. |
| `model` | `claude-opus-4-8`\* | strong tier (bug/security angles, large diffs). |
| `model_cheap` | = `model` | cheaper tier for cleanup angles on small diffs. |
| `fail_on` | `never` | advisory by default (green check + comments). Set `critical`/`major`/`any` to gate merges — the `Swatter` check goes red on confirmed findings. |
| `max_usd` | `5` | per-PR spend ceiling (priced models). |
| `max_tokens_total` | `8000000` | always-works ceiling for unknown-priced models. |
| `price_per_mtok_in`/`_out` | `0` | teach the ledger a custom model's price. |

\* No default for `openai-compat` — name your gateway's model.

## Safety

Swatter runs untrusted PR content (diffs, descriptions can be attacker-supplied
on public repos). The review agents are **read-only** — no shell, no network
tools, no GitHub token. Findings are typed JSON rendered by the harness, which
holds the token and does all posting. An instruction smuggled into a PR body
can't make the bot post, exfiltrate, or run anything.

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
