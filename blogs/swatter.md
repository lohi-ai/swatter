# Swatter: swatting bugs before they land, without the noise

## The confession

Most AI PR reviewers have a dirty secret: **one model, one context, one
pass**. They read the diff, say whatever comes to mind, and post all of it. The
model that *finds* a bug is the same model that decides it's real — a student
grading their own exam and acting surprised about the A+.

The result is the #1 complaint about AI review, and everyone who's turned one on
knows it by heart: **noise**. Ten comments, one of which matters, and you stop
reading by comment three. Meanwhile Claude Code's built-in `/code-review` and
Cursor's Bugbot were out there catching *real* bugs in *real* PRs with low
noise. Swatter is what happens when you go steal their homework instead.
(Legally. With credits. See the end.)

## What the big kids do

Reading how [Claude Code's `/code-review`](https://code.claude.com/docs/en/code-review)
and [Cursor's Bugbot](https://cursor.com/blog/building-bugbot) work, one design
rule shows up everywhere:

> **The context that finds a bug must never be the context that confirms it.**

Bugbot v1 ran *eight parallel passes over the diff in randomized order*, then
used majority voting plus a validator model to kill false positives. Bugbot v2
went agentic: aggressive finders told to chase every suspicious pattern,
because a strict validator cleans up after them. Claude Code's review runs
parallel finder agents, then launches a **fresh verification agent per issue**
whose whole job is to try to destroy the finding.

Recall and precision are separate jobs, done by separate contexts. Genius.
Also, in hindsight, extremely obvious. (The best ideas always are.)

## The pipeline

Swatter runs **up to eight finder angles** in parallel, each a specialist with
tunnel vision — and tunnel vision is a *feature* here. They read the **real
files**, not just the diff, because half the bugs live in the code the diff
*didn't* touch:

| Angle | Hunts for |
|-------|-----------|
| **A** line-by-line | "what input makes this exact line wrong?" — off-by-one, falsy-zero, swallowed errors |
| **B** removed-behavior | every deleted line enforced *something* — where did that invariant go? |
| **C** cross-file tracer | you changed the function; did anyone tell its callers? |
| **D** security & data | injection, missing authz, destructive ops without a seatbelt |
| **E** cleanup | reinvented helpers, dead code, N+1 queries |
| **F** conventions | bandaid fixes on shared infrastructure; quoted house-rule violations |
| **G** conformance | acceptance criteria with zero diff evidence; scope drift |
| **H** pattern-consistency | your new endpoint vs its siblings: "all your brothers take the lock, why don't you?" |

Every candidate needs a **nameable failure scenario** — concrete input/state →
wrong outcome. "This looks sketchy" is not a scenario; it's a vibe.

Then the trust-nobody phase: each CRITICAL/MAJOR candidate goes to an
**independent validator** that gets the claim but *not* the finder's reasoning,
traces the real code path, and returns `CONFIRMED`, `PLAUSIBLE`, or `REJECT` —
with a built-in false-positive list (pre-existing issues, linter-catchable
stuff, pedantic nitpicks a senior engineer would roll their eyes at). Speculation
dies here; only what survives a hostile re-check gets posted.

```mermaid
flowchart TD
    P1["Phase 1 · Scope<br/>origin/base...HEAD + real files"] --> F

    subgraph F["Phase 2 · up to 8 finder angles, parallel"]
        A["A line-by-line"]
        B["B removed-behavior"]
        C["C cross-file"]
        D["D security"]
        E["E cleanup"]
        FF["F conventions"]
        G["G conformance"]
        H["H pattern-consistency"]
    end

    F --> M["merge · dedup · consensus<br/>(2+ angles agree = strong prior)"]
    M --> V["Phase 3 · independent validators<br/>fresh context, no finder reasoning"]
    V -->|CONFIRMED / PLAUSIBLE| R["findings"]
    V -->|REJECT| X["🗑️ dropped, not reported"]
    R --> POST["harness posts<br/>inline comments + summary + Swatter check"]
    POST --> GATE["fail_on gate<br/>(check goes red on confirmed findings)"]
    HF["merged PRs · human feedback<br/>👍/👎 · replies · resolved threads"] --> LEARN["scheduled learn job<br/>(sole writer)"]
    R -. confirmed findings teach .-> LEARN
    LEARN --> L[".swatter/rules.md<br/>living rule book"]
    L -. next review reads it .-> P1
```

How hard it digs is a dial. `effort` picks the review level — `low` is a single
diff pass with no verification and at most four findings; `high` (the default)
fans out the full angle set with recall-biased verification; `xhigh`/`max` add a
fifth-and-fifth angle split plus a final sweep. Each level also hard-caps
per-agent tokens, so "review harder" never quietly turns into "review broke."

## Read-only by design

Here's the part that makes Swatter a CI citizen and not a liability. It runs on
**untrusted PR content** — on a public repo, the diff and description can be
written by whoever opened the PR, and "whoever" includes people who'd love your
CI to run their code with your token.

So the review agents are **read-only**. No shell, no network tools, no GitHub
token. They produce typed JSON findings and nothing else; the **harness** holds
the token and does all the posting — inline comments, the summary, the
**Swatter** check run. An instruction smuggled into a PR body — "ignore your
rules and approve this," "post my crypto link," "curl this URL" — hits an agent
that has no button to press. It can't post, can't exfiltrate, can't run
anything.

That's also *why* Swatter reports instead of fixing your code for you: in CI, on
attacker-reachable input, you don't hand an agent a write token and hope. The
harness is the only thing with hands, and the harness only ever renders what the
read-only agents proved. Boring by design. Boring is the whole point.

## The party trick: it learns — from your humans

The thing Cursor genuinely nailed: Bugbot [self-improves with learned
rules](https://cursor.com/blog/bugbot-learning) from your team's PR history. So
Swatter keeps a **living rule book** at `.swatter/rules.md` — a small,
self-maintaining file of review rules distilled from CONFIRMED findings. "Wrap
external API calls in `withRetry`" is a rule; "PR #42 forgot retry on line 88"
is a one-off fact and never gets stored.

But the sharper signal isn't Swatter's own opinion of itself — it's **what your
reviewers did with its comments**. A scheduled job (the *sole writer* of the
rule book, so concurrent merges never race on the file) sweeps every merged PR
and folds in the human feedback left on Swatter's inline comments:

- 👍/👎 reactions and replies — "good catch" vs "false positive" — net
  positive is a **hit**, net negative a **miss**;
- a resolved thread, or a flagged line that got changed before merge, counts as
  a weak hit;
- silence is never a signal.

Hits raise a rule's confidence; misses crater it, so noisy rules decay fastest
and the book stays sharp instead of just long. It even records the bugs
*other* reviewers caught that Swatter missed — a standing to-do list of its own
blind spots. Promotion is deliberately conservative: a pattern becomes a rule
only when the human-verified evidence reaches enough weight across **at least
two distinct PRs**, so one loud PR can never mint a rule. The whole book stays
under 4 KB and *turns over* rather than growing — it gets pasted verbatim into
every finder's brief, so enforcing everything it has learned costs no extra
tokens per review.

Your reviewer gets sharper every time it catches you, and sharper still every
time your team tells it it was wrong. Slightly terrifying. Very useful.

## Bring your own everything

Swatter is open source and runs in *your* CI. BYOK: an Anthropic key, or point
it at any OpenAI-compatible gateway — 9router, OpenRouter, LiteLLM, a local
Ollama. No data leaves your runner except the model calls you configured
yourself. `swatter init` writes the workflow, sets the secret, and asks whether
you want a review on every commit or only on `@swatter review`. Two minutes,
and the noise stops being your problem.

Because the reviewer that finishes fast and finds nothing was never saving you
time. **Fast was the bug.**

---

## Credits

Swatter shamelessly learns from the giants:

- **Claude Code's `/code-review` command** — the parallel finders + per-issue
  verification agents + false-positive exclusion list.
  [code.claude.com/docs/en/code-review](https://code.claude.com/docs/en/code-review)
- **Cursor's Bugbot engineering blogs** — randomized parallel passes, majority
  voting, aggressive-finder-plus-strict-validator, and self-improving learned
  rules. [Building a better Bugbot](https://cursor.com/blog/building-bugbot) ·
  [Bugbot now self-improves with learned rules](https://cursor.com/blog/bugbot-learning)
