# Plan — Swatter: OSS Cursor-Bugbot alternative on agentcore

**Name: Swatter** — a fly swatter kills bugs on contact. Short, fun, verb-able
("Swatter swatted 2 CRITICAL bugs"), clean as CLI (`swatter`), check-run name,
and image (`ghcr.io/lohi-ai/swatter`). No GitHub/product collision found
(2026-07-11; runner-ups: bugbane, flytrap; rejected zap* — OWASP ZAP collision).

## Goal
A general PR-review bugbot that installs in one workflow file: on every PR it
builds a review packet, runs the review-pr finder/validator pipeline on
agentcore, posts inline PR comments + a check run, and **maintains its own rule
book** — learning new rules from confirmed findings, deduping, compacting, and
expiring stale ones. Distribution: Docker image + GitHub Action first; GitHub
App mode later. **BYOK is the default and only mode** — there is no hosted
inference; users bring an Anthropic key or point at any OpenAI-compatible
gateway (9router, OpenRouter, LiteLLM, Ollama, …). **Independent project in its own repo
(`github.com/lohi-ai/swatter`), consuming agentcore as a normal Go
dependency from `github.com/lohi-ai/agentray`.**

## OSS landscape (investigated 2026-07-11)
- **[qodo-ai/pr-agent](https://github.com/qodo-ai/pr-agent)** — ~11.5k★, Apache-2.0, BYOK, Action/App/CLI, slash commands. The incumbent. Single-pass prompt review; "learning" is a static best-practices config file — no validation pass, no rule lifecycle.
- **[kodustech/kodus-ai](https://github.com/kodustech/kodus-ai)** — ~1.2k★, AGPL, self-hosted, natural-language custom rules, AST-assisted. Heavy install (installer repo, 15–30 min, own Postgres).
- **[anthropics/claude-code-action](https://github.com/anthropics/claude-code-action)** — official; runs full Claude Code in Actions, has automated-review recipes; general-purpose, not a reviewer product; no finding validation, no learned-rule store. ~$0.5–15/PR.
- **[anthropics/claude-code-security-review](https://github.com/anthropics/claude-code-security-review)** — security-only Action.
- coderabbitai/ai-pr-reviewer — archived. CodeRabbit/Greptile/Panto — commercial, closed.

**Gap we fill**: (1) validated findings — independent validator pass ⇒ low
noise (every OSS tool above is single-pass, noise is their #1 complaint);
(2) a *living* rule book with dedup/compaction/expiry instead of an
append-only or hand-edited config. Both come straight from review-pr, which
none of these tools have.

## Scope
- **Sub-ticket 0 (prereq): agentray → its own GitHub repo + importable agentcore.**
  1. Promote `internal/agentcore` → `agentcore/` and `internal/sandbox` → `sandbox/` (both are leaf packages by design — *derived*, `docs/ARCHITECT-AGENT-BOUNDARY.md` reference in sandbox.go — so a mechanical move). Without it no external repo can import them (Go `internal/` rule).
  2. **No module rename needed** — go.mod already declares `github.com/lohi-ai/agentray`, which is exactly why the lohi-ai org was chosen (decided 2026-07-11; name verified available) over the personal account: every existing import line stays untouched.
  3. Create the **lohi-ai org**, then `gh repo create lohi-ai/agentray --public` (**both repos public OSS from day one** — agentray already carries an MIT LICENSE) and push agentray as a **squashed snapshot** (not subtree history — the monorepo history under `agentray/` has not been secret-audited; scrub + gitleaks pass is mandatory before the first public push). Tag `v0.1.0` so swatter can pin it.
  4. kiem-lai remains the working monorepo; the lohi-ai/agentray repo is the **module mirror**: re-push + re-tag from `agentray/` on each release (one `scripts/publish-module.sh`, or manual at first). Source of truth stays kiem-lai until agentray itself migrates out (not this plan).
- New repo `github.com/lohi-ai/swatter` (`gh repo create --public`, local: `/Users/long/workspace/lohi/swatter`), own go.mod, `require github.com/lohi-ai/agentray v0.1.0`, importing `.../agentcore` and `.../sandbox`. Both repos public — no GOPRIVATE/PAT anywhere; anyone can `go build` swatter from source. License: Apache-2.0 for swatter (patent grant, same as pr-agent — the norm in this category; agentray stays MIT).
- Workspace-guarded **read-only** toolset for agents (read_file, grep, glob — exported constructors exist in `sandbox/{file,search}_tool.go`), rooted at the Action's repo checkout. **No shell tool in MVP**: a plain local exec inside the Action container cannot be network-denied (the container must have network for model calls, and Docker's default seccomp blocks netns tricks), so the earlier "run_shell, no network" idea is unenforceable — the harness runs all git commands itself and agents only read. Shell returns in phase 2 behind a real sandbox.
- Ported review-pr pipeline, **harness-orchestrated** (Go drives phases; each finder/validator is its own bounded agentcore run) — deterministic caps and cost, no orchestrator-agent drift.
- **Stolen from anthropics/claude-code-action** (their onboarding + UX is the category's best; verified against the repo 2026-07-11):
  - `swatter init` — one-command onboarding (their `/install-github-app` moment): asks provider (Anthropic / OpenAI-compat gateway + base_url), generates the workflow file, runs `gh secret set SWATTER_API_KEY`, prints the branch-protection tip. No OSS reviewer has this; pr-agent/kodus onboarding is copy-paste YAML + docs spelunking.
  - **Live progress comment**: the sticky comment posts immediately and updates with checkboxes as phases complete (`[x] packet → [x] finders 8/8 → [ ] validators 2/5…`), then becomes the final summary. Harness-side only — the agent never writes it.
  - **Mention re-trigger**: an `issue_comment` recipe fires the same pipeline on `@swatter review` (fresh review of current head; idempotent reporter makes this free). Full slash-command surface stays out of scope.
  - **Action outputs + artifact**: findings JSON exposed as a step output (`steps.swatter.outputs.findings`) for downstream jobs, and the full run transcript uploaded as a workflow artifact for debugging.
  - **Concurrency recipe**: documented `concurrency: swatter-${{ github.head_ref }}` + `cancel-in-progress` so a re-push cancels the superseded (billed) review.
  - Phase 2 steals, not now: OIDC/workload-identity auth, Bedrock/Vertex providers, GitHub-App-minted short-lived tokens.
- GitHub reporting: check run "Swatter" (conclusion mapped by a `fail_on` input: `critical` | `major` (default) | `any` | `never`) + inline review comments + the live-progress sticky comment above, idempotent across re-pushes. Inline comments are only possible on lines present in the diff — and finders legitimately report on *unchanged* lines of touched functions — so the reporter maps each finding to a diff position and falls back to the summary comment (with a permalink) when the line isn't in a hunk. Fork PRs (read-only `GITHUB_TOKEN`) are detected up front → log + exit neutral, never a red check.
- Rules store `.swatter/rules.md` (structured entries) + learn/dedup/compact/expire lifecycle.
- Back-port the same rule-lifecycle format + compaction step into the babysit review-pr skill (`.babysit/review-pr.md` + SKILL.md Learn section).
- Docker image (GHCR) + `action.yml` + example workflow + README quickstart.
- **Out**: GitHub App/webhook mode (phase 2 — designed for, not built); GitLab/Bitbucket; auto-fix (Phase 4 of review-pr — Swatter reports only, Cursor-Bugbot parity); web dashboard/agentray UI integration; slash commands beyond the `@swatter review` re-trigger; OIDC/Bedrock/Vertex auth (phase 2).
- *Derived risk carried to Risks*: prompt injection via PR diff/description; fork PRs with no secrets access.

## Approach
**Data flow (Action mode)**
```
pull_request event → actions/checkout (fetch-depth: 0)
  → docker://ghcr.io/lohi-ai/swatter  (swatter run --github-event $GITHUB_EVENT_PATH)
    1. Packet: git diff origin/BASE...HEAD → branch.diff; brief.md (PR title/body
       as *untrusted data*, changed-file table, CLAUDE.md/AGENTS.md paths,
       rules book pasted verbatim)
    2. Finders: N parallel agentcore runs (angles A–H from review-pr, packed by
       diff size exactly per SKILL.md phase-2 rules). Tools: read_file/grep/glob
       only (Workspace=checkout, read-only; no shell — see Scope). Output:
       JSON candidates {file,line,summary,failure_scenario} via agentcore
       structured-output (schema.go).
    3. Validators: dedup → one fresh agentcore run per CRITICAL/MAJOR candidate
       (no finder reasoning passed) → CONFIRMED/PLAUSIBLE/REJECT. Sweep on
       >500 lines or any CRITICAL.
    4. Report: harness (not the agent) posts via GitHub REST — agent never holds
       the token, so injected instructions can't post/exfiltrate.
    5. Learn: rule-lifecycle pass (below); emits .swatter/rules.md update as a
       PR-comment patch suggestion or optional bot commit (opt-in input).
```
**Providers (BYOK, config-only)**: action inputs `provider: anthropic (default)
| openai-compat`, `api_key` (secret, required), `base_url` (openai-compat
gateways — 9router, OpenRouter, LiteLLM, Ollama), `model` +
optional `models_strong`/`models_cheap` for the per-angle tiering review-pr
prescribes (A–D strong, E–G may run a tier down on small diffs). Verified in
`agentcore/openai.go`: custom `BaseURL`, per-vendor `Compat` table, and SSE
folding on non-streaming replies — the exact 9router quirk — already exist, so
gateway support is pure config, zero provider code.

**agentcore mapping**: one `agentcore.Agent` per phase-run — `BudgetGate` from
`--max-usd` input (shared cost ledger across runs), `Escalation` ladder from
config, `MaxTurns`/`MaxToolCalls` limits, prompt caching keyed per PR. The
budget is a **shared atomic ledger** in pipeline.go (BudgetGate is per-Agent;
parallel runs must debit one counter). Finder charters ship as embedded
markdown injected via `AgentDefinition.Soul` (finder preamble) +
`AgentDefinition.Agents` (the angle charter) — **not** as agentcore Skills:
skills are progressive-disclosure (the model must choose to `read_skill`), and
a finder's charter must be unconditionally in context; both slots are
always-loaded with an 8 KB cap each, which charters fit easily. No
`spawn_subagent` needed in MVP (harness fans out); keep `Subagents: nil`.

**Rule book — the differentiator.** `.swatter/rules.md` entries:
```markdown
- id: r-2026-07-11-a1
  rule: <one-sentence rule a future diff could violate>
  origin: PR#42 2026-07-11   confidence: 0.9
  hits: 3   last_hit: 2026-07-10   misses: 12
```
Lifecycle, run after Report each review:
1. **Learn**: each CONFIRMED finding → candidate rule (generalized, never a
   one-off fact — same bar as review-pr's Learn).
2. **Dedup**: LLM same-rule judge against existing book before insert (the
   Litrans bible near-dup pattern: exact match is not enough).
3. **Score**: finders report which rules fired; `hits`/`misses`/`last_hit`
   updated every review. A validator-REJECTed finding that a rule produced
   decrements confidence (rules that generate noise decay fastest).
4. **Compact/expire**: when book > 4 KB or every 20 reviews — merge near-dups,
   tighten wording, evict lowest score = confidence × recency-decay; a rule
   citing a path/symbol no longer in the repo is expired immediately.
The whole book (≤4 KB) is pasted into every finder brief, so cost stays flat.

**Back-port to review-pr skill**: same entry format for `.babysit/review-pr.md`;
SKILL.md Learn section gains the dedup-judge step and a compaction trigger
(size/age), so both consumers share one rule-lifecycle spec (put it in
`swatter/docs/DESIGN-RULEBOOK.md`, referenced by both).

**GitHub App mode (phase 2, design-only now)**: standalone `swatter serve`
webhook mode (same binary), clones shallow to a workspace, same pipeline; rules
move to Postgres per repo; feedback signals (👍/👎 reactions, comment
resolved-vs-dismissed, was-the-line-changed-before-merge) feed rule confidence.
MVP keeps every interface (RuleStore, Reporter, Source) so App mode is new
adapters only.

## Reuse
- `github.com/lohi-ai/agentray/agentcore` (post-promotion, pinned tag) — loop, providers (anthropic.go/openai.go), limits, budget gate, escalation, structured output (schema.go), skills. No core changes expected.
- `github.com/lohi-ai/agentray/sandbox` (post-promotion) — Workspace path guard + `NewReadFileTool`/`NewGrepTool`/`NewGlobTool` (verified exported, take `*Workspace`, backend-agnostic). No Sandbox backend needed in MVP (read-only toolset, no shell).
- `agentray/internal/agentruntime/toolregistry.go` pattern (copied, not imported — it stays internal) for building the per-phase ToolSet.
- review-pr SKILL.md — phases, angle charters, validator verdict grammar, finding format ported verbatim into finder/validator skill files.
- **New**: everything under `swatter/` (packet builder, pipeline, rule lifecycle, GitHub reporter). New because nothing existing drives agentcore headlessly against a git checkout or talks to the GitHub review API.

## Files
- `agentray/`: `internal/agentcore` → `agentcore/`, `internal/sandbox` → `sandbox/`, go.mod module rename `github.com/lohi-ai/agentray` → `github.com/lohi-ai/agentray` + import rewrites, `scripts/publish-module.sh` (sub-ticket 0, own PR in kiem-lai + first push/tag to lohi-ai/agentray)
- `swatter/cmd/swatter/main.go` — subcommands `run` (CI) + `init` (onboarding); flags/env parsing, event decode, exit code = check conclusion
- `swatter/internal/{packet.go,pipeline.go,findings.go,rules.go,report_github.go,progress.go,initcmd.go}`
- `swatter/docs/recipes.md` — copy-paste workflows: default PR review, `@swatter review` re-trigger, path-filtered review, concurrency-cancel (their solutions.md pattern)
- `swatter/internal/skills/*.md` — finder charters, validator prompt, learn prompt (embedded, agentcore embed.go pattern)
- `swatter/{Dockerfile,action.yml}` + `swatter/.github/workflows/release-image.yml` (GHCR publish to ghcr.io/lohi-ai/swatter)
- `swatter/docs/DESIGN-RULEBOOK.md` — rule-lifecycle spec (shared with review-pr)
- `/Users/long/workspace/lohi/babysit/.claude/skills/review-pr/SKILL.md` — Learn section upgrade (separate repo, own commit)

## Verification
- Sub-ticket 0: `cd agentray && go build ./... && go test ./agentcore/... ./sandbox/...` (green = pure move); then from a scratch dir outside the monorepo: `go mod init t && go get github.com/lohi-ai/agentray@v0.1.0 && go build` a 5-line program constructing `agentcore.New` — proves the module actually resolves.
- Gitleaks on the snapshot before the first push — `.env` must not ship, and tracked `infra/gce/*/app.env` (real `DEFAULT_PROJECT_API_KEY`) must be excluded or placeholdered.
- `cd swatter && go test ./...`
- Fixture replay: `go test ./internal -run TestPipelineFixture` — a vendored mini-repo + diff with 3 planted bugs (nil-deref, removed guard, SQL injection) + 1 decoy; assert ≥2 planted found, decoy rejected, findings JSON schema valid (agentcore's realprovider_test pattern for a live-model variant behind an env flag).
- Rule lifecycle unit tests: dedup rejects paraphrase, compaction evicts by score, path-gone expiry.
- Live: `docker build`, then run the Action from a test workflow on a real kiem-lai PR branch; confirm inline comments + check run "Swatter" + idempotent re-push.

## Risks
- **Prompt injection** (PR body/diff are attacker-controlled on public repos): mitigated — agent has no token, no shell, no write tools, no network tools; findings are typed JSON rendered by the harness; brief marks author text as data (review-pr preamble already does). Fork PRs: document `pull_request_target` pitfalls; default recipe uses `pull_request` (no secrets on forks — Swatter detects the read-only token and exits neutral).
- **Secret leak on first push**: agentray has a live `.env` (gitignored — stays out) but the **tracked** `infra/gce/{dev,prod}/app.env` carry a real `DEFAULT_PROJECT_API_KEY` and internal infra addresses (verified 2026-07-11) — exclude `infra/` from the published snapshot (module consumers don't need deploy config) or placeholder those values, and gitleaks the snapshot; never subtree-push raw monorepo history without an audit.
- **Mirror drift**: kiem-lai stays the source of truth; the lohi-ai/agentray repo only advances via publish-module + tag. Swatter pins exact tags, so drift breaks nothing — it just means swatter waits for the next publish. If agentray development ever moves to the new repo wholesale, kiem-lai's copy must be retired to avoid two-way edits (out of scope here).
- **Public from day one raises the scrub bar**: a leaked key in the first push is public instantly and cached by the Go module proxy (proxy.golang.org keeps immutable copies of tagged versions — a bad tag can't be untagged away; rotate the key AND cut a new tag). The `DEFAULT_PROJECT_API_KEY` in the tracked app.env files should be rotated regardless, since it has sat in the private monorepo history.
- **Cost/latency per PR**: 8 finders + validators is heavier than pr-agent's single call. Bounded by `--max-usd` budget gate + diff-size packing rules; document expected $1–5/PR; small diffs pack angles into 2–3 runs.
- **Rule-book write-back from CI**: committing from an Action is footgunny (loops, protected branches, and two concurrent PRs racing on `.swatter/rules.md`). Default = suggestion comment (human-mediated, race-free); direct commit strictly opt-in with `[skip ci]` + fetch-rebase-retry.
- **Angles C and G lose reach without shell** (C traces callers of changed symbols; grep covers most of it, but no `go build`/tests; G can't run acceptance checks). Accepted MVP trade-off — grep+read gets ~90% of finder value and keeps the injection posture airtight; revisit with the phase-2 sandbox.
- **`git diff` correctness in container**: checkout needs `fetch-depth: 0` and `safe.directory` config — handle in action.yml, test in live run.
- **Budget gate vs unknown gateway models**: `agentcore/pricing.go` prices known models; a 9router/custom model id it doesn't know would cost-account as $0 and `--max-usd` would never fire. Mitigate: optional `price_per_mtok_in/out` inputs, plus a token-count ceiling (`--max-tokens-total`) as the always-works backstop.

## Next
Sub-tickets, ordered, each independently verifiable:
0. **agentray-publish** (in kiem-lai) — promote agentcore + sandbox out of `internal/`, module rename to `github.com/lohi-ai/agentray`, create the GitHub repo, push scrubbed squashed snapshot, tag `v0.1.0`, verify external `go get` (build+tests green, module resolves).
1. **swatter-core** — `gh repo create lohi-ai/swatter --public` + scaffold + cmd/swatter + packet + read-only toolset wiring + finder/validator pipeline + shared budget ledger + findings JSON (fixture replay green).
2. **swatter-github** — Dockerfile + action.yml (outputs incl. findings JSON) + GHCR workflow + reporter with live-progress sticky comment + idempotent re-push + `swatter init` + recipes.md incl. `@swatter review` re-trigger (live PR test green, incl. one mention re-trigger).
3. **swatter-rules** — rule store + learn/dedup/score/compact lifecycle + DESIGN-RULEBOOK.md + review-pr SKILL.md back-port (lifecycle unit tests green).
4. *(phase 2, not scheduled)* **swatter-app** — `swatter serve` GitHub App webhook mode (App-minted short-lived tokens), Postgres rules, reaction-feedback signals, OIDC + Bedrock/Vertex providers, sandboxed shell for finder angles C/G.
