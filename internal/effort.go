package internal

import "github.com/lohi-ai/agentray/agentcore"

// Effort selects the review level per the reference level table:
//
//	| level  | pipeline                                                        |
//	|--------|-----------------------------------------------------------------|
//	| low    | 1 diff pass → no verify → ≤4 findings                           |
//	| medium | 3+5 angles × 6 candidates → 1-vote verify → ≤8 findings (precision) |
//	| high   | 3+5 angles × 6 candidates → 1-vote verify (recall-biased) → ≤10 findings |
//	| xhigh  | 5+5 angles × 8 candidates → 1-vote verify → sweep → ≤15 findings |
//	| max    | same as xhigh; only the API reasoning effort differs, not the fan-out |
//
// "3+5"/"5+5" is correctness angles + the five cleanup lenses; Swatter folds
// the lenses into one cleanup agent (see runFinders). Each level also carries
// a hard per-agent token cap and matching per-phase Limits, sized so the cap
// provably holds (high stays under 120K tokens per agent; medium and low
// under that).
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"

	// EffortAuto is not a pipeline level: it tells the run to size the level
	// from the diff (see resolveEffort). It is resolved to a concrete level in
	// RunReview before the pipeline sees it, so EffortProfile never runs on it.
	EffortAuto Effort = "auto"
)

// resolveEffort picks a concrete level from the diff's size — the number of
// changed files and total changed (± ) lines — when the configured effort is
// EffortAuto. It uses the higher of the two dimensions so one large file or
// many small files each lift the level; the thresholds are the "leaner"
// (cheaper) tier so most PRs land at low/medium and only sprawling changes
// reach xhigh:
//
//	≤3 files  AND ≤50 lines   → low
//	≤10 files AND ≤300 lines  → medium
//	≤25 files AND ≤1000 lines → high
//	larger                    → xhigh
//
// max is never auto-selected — it only raises the provider reasoning effort at
// the same fan-out as xhigh, a deliberate opt-in.
func resolveEffort(files, lines int) Effort {
	switch {
	case files <= 3 && lines <= 50:
		return EffortLow
	case files <= 10 && lines <= 300:
		return EffortMedium
	case files <= 25 && lines <= 1000:
		return EffortHigh
	default:
		return EffortXHigh
	}
}

// maxOutputTokens is the per-completion cap roleAgent sets on every phase. It
// is part of the per-agent budget math below, so the two must move together.
const maxOutputTokens = 8192

// PhaseLimits are one level's agentcore Limits per phase shape.
type PhaseLimits struct {
	Scope      agentcore.Limits // change summary + conventions (cheap tier)
	Finder     agentcore.Limits // correctness angles, cleanup, sweep
	Verify     agentcore.Limits // per-location validators
	WrapUp     agentcore.Limits // single toolless salvage turn for truncated runs
	Synthesize agentcore.Limits // single toolless merge/rank turn
	Briefing   agentcore.Limits // single-turn reviewer briefing (whole diff inline)
	Learn      agentcore.Limits // rule generalization + feedback clustering
	Judge      agentcore.Limits // single-turn same-rule YES/NO judge
}

// EffortProfile is one level's full recipe: the pipeline shape from the level
// table plus the per-agent budget. The budget invariant (checked in
// effort_test.go) is that no single run can bill PerAgentTokens or more:
//   - a multi-turn phase is gated at gateTokens(cap, limits) — see there;
//   - a single-turn phase bills at most MaxContextTokens + maxOutputTokens,
//     which every entry keeps below the cap.
type EffortProfile struct {
	// Pipeline shape.
	Angles        []string // correctness angles that run (subset of A–E)
	PerAngle      int      // candidate cap per correctness angle
	Cleanup       bool     // run the five-lens cleanup agent
	Verify        bool     // run the per-location verifiers (low reports raw)
	ConfirmedOnly bool     // precision: only CONFIRMED verdicts survive (medium)
	Sweep         bool     // gap-hunting sweep after verify (xhigh/max)
	Scope         bool     // LLM scope pre-pass (skipped at low)
	Synthesize    bool     // LLM merge/rank pass (skipped at low)
	Briefing      bool     // LLM reviewer briefing (skipped at low — "1 diff pass")
	MaxFindings   int      // report cap
	// ReasoningEffort is passed to providers with a thinking-effort knob (max
	// only — the one thing that separates max from xhigh). Empty = provider default.
	ReasoningEffort string

	// PerAgentTokens is the ceiling on one role agent's total billed tokens
	// (input + output + cache reads/writes, the ledger's tokens() measure).
	PerAgentTokens int
	Limits         PhaseLimits
}

// EffortProfile resolves the config's effort level to its recipe.
func (c Config) EffortProfile() EffortProfile {
	switch c.Effort {
	case EffortLow:
		// 1 diff pass → no verify → ≤4 findings. Angle A is the line-by-line
		// diff scan — literally the single pass; every other phase is off.
		return EffortProfile{
			Angles: []string{"A"}, PerAngle: 6, MaxFindings: 4,
			PerAgentTokens: 40_000,
			Limits: PhaseLimits{
				Scope:      agentcore.Limits{MaxTurns: 4, MaxToolCalls: 8, MaxToolResultLen: 8_000, MaxContextTokens: 8_000},
				Finder:     agentcore.Limits{MaxTurns: 6, MaxToolCalls: 12, MaxToolResultLen: 8_000, MaxContextTokens: 8_000},
				Verify:     agentcore.Limits{MaxTurns: 5, MaxToolCalls: 10, MaxToolResultLen: 8_000, MaxContextTokens: 8_000},
				WrapUp:     agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 8_000, MaxContextTokens: 12_000},
				Synthesize: agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 8_000, MaxContextTokens: 16_000},
				Briefing:   agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 2_000, MaxContextTokens: 30_000},
				Learn:      agentcore.Limits{MaxTurns: 2, MaxToolCalls: 2, MaxToolResultLen: 6_000, MaxContextTokens: 8_000},
				Judge:      agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 2_000, MaxContextTokens: 8_000},
			},
		}
	case EffortMedium:
		// 3+5 angles × 6 → 1-vote verify → ≤8 findings, tuned for precision:
		// only CONFIRMED verdicts survive.
		return EffortProfile{
			Angles: []string{"A", "B", "C"}, PerAngle: 6, Cleanup: true,
			Verify: true, ConfirmedOnly: true, Scope: true, Synthesize: true,
			Briefing:       true,
			MaxFindings:    8,
			PerAgentTokens: 72_000,
			Limits: PhaseLimits{
				Scope:      agentcore.Limits{MaxTurns: 6, MaxToolCalls: 16, MaxToolResultLen: 12_000, MaxContextTokens: 12_000},
				Finder:     agentcore.Limits{MaxTurns: 8, MaxToolCalls: 20, MaxToolResultLen: 12_000, MaxContextTokens: 16_000},
				Verify:     agentcore.Limits{MaxTurns: 8, MaxToolCalls: 16, MaxToolResultLen: 12_000, MaxContextTokens: 14_000},
				WrapUp:     agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 12_000, MaxContextTokens: 24_000},
				Synthesize: agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 12_000, MaxContextTokens: 24_000},
				Briefing:   agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 2_000, MaxContextTokens: 60_000},
				Learn:      agentcore.Limits{MaxTurns: 3, MaxToolCalls: 2, MaxToolResultLen: 8_000, MaxContextTokens: 16_000},
				Judge:      agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 2_000, MaxContextTokens: 8_000},
			},
		}
	case EffortXHigh, EffortMax:
		// 5+5 angles × 8 → 1-vote verify → sweep → ≤15 findings. max is the
		// same fan-out with the provider's reasoning effort turned up.
		p := EffortProfile{
			Angles: []string{"A", "B", "C", "D", "E"}, PerAngle: 8, Cleanup: true,
			Verify: true, Sweep: true, Scope: true, Synthesize: true,
			Briefing:       true,
			MaxFindings:    15,
			PerAgentTokens: 160_000,
			Limits: PhaseLimits{
				Scope:      agentcore.Limits{MaxTurns: 8, MaxToolCalls: 24, MaxToolResultLen: 16_000, MaxContextTokens: 24_000},
				Finder:     agentcore.Limits{MaxTurns: 16, MaxToolCalls: 48, MaxToolResultLen: 16_000, MaxContextTokens: 32_000},
				Verify:     agentcore.Limits{MaxTurns: 12, MaxToolCalls: 32, MaxToolResultLen: 16_000, MaxContextTokens: 28_000},
				WrapUp:     agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 16_000, MaxContextTokens: 40_000},
				Synthesize: agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 16_000, MaxContextTokens: 40_000},
				Briefing:   agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 2_000, MaxContextTokens: 140_000},
				Learn:      agentcore.Limits{MaxTurns: 3, MaxToolCalls: 2, MaxToolResultLen: 8_000, MaxContextTokens: 32_000},
				Judge:      agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 2_000, MaxContextTokens: 8_000},
			},
		}
		if c.Effort == EffortMax {
			p.ReasoningEffort = "high"
		}
		return p
	default: // EffortHigh
		// Same fan-out as medium, tuned for recall: PLAUSIBLE verdicts survive
		// and the report cap is higher.
		return EffortProfile{
			Angles: []string{"A", "B", "C"}, PerAngle: 6, Cleanup: true,
			Verify: true, Scope: true, Synthesize: true,
			Briefing:       true,
			MaxFindings:    10,
			PerAgentTokens: 110_000,
			Limits: PhaseLimits{
				Scope:      agentcore.Limits{MaxTurns: 8, MaxToolCalls: 24, MaxToolResultLen: 16_000, MaxContextTokens: 16_000},
				Finder:     agentcore.Limits{MaxTurns: 12, MaxToolCalls: 32, MaxToolResultLen: 16_000, MaxContextTokens: 24_000},
				Verify:     agentcore.Limits{MaxTurns: 10, MaxToolCalls: 24, MaxToolResultLen: 16_000, MaxContextTokens: 20_000},
				WrapUp:     agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 16_000, MaxContextTokens: 32_000},
				Synthesize: agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 16_000, MaxContextTokens: 32_000},
				Briefing:   agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 2_000, MaxContextTokens: 100_000},
				Learn:      agentcore.Limits{MaxTurns: 3, MaxToolCalls: 2, MaxToolResultLen: 8_000, MaxContextTokens: 24_000},
				Judge:      agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 2_000, MaxContextTokens: 8_000},
			},
		}
	}
}

// gateTokens returns the run-usage threshold at which a role agent's budget
// gate fires so its total stays under cap. The gate is consulted at the top of
// each turn with the run's accumulated usage; after it passes at just under
// the threshold the loop can run one more exploring turn and then the toolless
// finalize turn, each billing at most MaxContextTokens (the compacted
// transcript re-sent as input, cache reads included) + maxOutputTokens. So the
// threshold reserves two such turns: total < gate + 2·(context + completion) ≤ cap.
// A non-positive result is returned as 0, which fires the gate immediately —
// the degenerate one-finalize-turn run still bills under one turn's reserve.
func gateTokens(cap int, l agentcore.Limits) int {
	at := cap - 2*(l.MaxContextTokens+maxOutputTokens)
	if at < 0 {
		return 0
	}
	return at
}

// wrapUpMinContext is the smallest context window in which a salvage turn can
// still re-read enough of its transcript to conclude usefully. When the cap's
// remaining headroom falls below it the wrap-up is skipped entirely.
const wrapUpMinContext = 6_000

// wrapUpLimits bounds the single toolless salvage turn for a truncated finder,
// sweep, or verifier so the PARENT run's spend plus this wrap-up turn stays
// under the per-agent cap. A finder gate only meters its own run, so without
// this the parent's near-cap run plus a fresh wrap-up run could bill past
// PerAgentTokens for one logical angle. The wrap-up turn bills at most
// MaxContextTokens + maxOutputTokens, so shrinking its context to the cap's
// remaining headroom keeps parent + wrap-up within the cap. Returns ok=false
// when too little headroom remains for a useful turn — the caller then drops
// the angle rather than exceed the cap.
func (p EffortProfile) wrapUpLimits(parentSpent int) (agentcore.Limits, bool) {
	lim := p.Limits.WrapUp
	headroom := p.PerAgentTokens - parentSpent - maxOutputTokens
	if headroom < wrapUpMinContext {
		return lim, false
	}
	if headroom < lim.MaxContextTokens {
		lim.MaxContextTokens = headroom
	}
	return lim, true
}
