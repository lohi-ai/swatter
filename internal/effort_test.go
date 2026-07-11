package internal

import (
	"testing"

	"github.com/lohi-ai/agentray/agentcore"
)

func profile(e Effort) EffortProfile { return Config{Effort: e}.EffortProfile() }

// phaseLimits enumerates a profile's per-phase Limits for invariant checks.
func phaseLimits(p EffortProfile) map[string]agentcore.Limits {
	return map[string]agentcore.Limits{
		"scope":      p.Limits.Scope,
		"finder":     p.Limits.Finder,
		"verify":     p.Limits.Verify,
		"wrapup":     p.Limits.WrapUp,
		"synthesize": p.Limits.Synthesize,
		"briefing":   p.Limits.Briefing,
		"learn":      p.Limits.Learn,
		"judge":      p.Limits.Judge,
	}
}

// TestEffortProfiles_LevelTable pins each level's pipeline shape to the
// reference table: low `1 diff pass → no verify → ≤4`; medium `3+5 × 6 →
// verify → ≤8` (precision); high the same fan-out recall-biased → ≤10; xhigh
// `5+5 × 8 → verify → sweep → ≤15`; max = xhigh + API reasoning effort.
func TestEffortProfiles_LevelTable(t *testing.T) {
	low, med, high := profile(EffortLow), profile(EffortMedium), profile(EffortHigh)
	xhigh, max := profile(EffortXHigh), profile(EffortMax)

	if len(low.Angles) != 1 || low.Cleanup || low.Verify || low.Sweep || low.Scope || low.Synthesize || low.MaxFindings != 4 {
		t.Errorf("low must be one unverified diff pass capped at 4 findings, got %+v", low)
	}
	for _, tc := range []struct {
		name string
		p    EffortProfile
	}{{"medium", med}, {"high", high}} {
		if len(tc.p.Angles) != 3 || tc.p.PerAngle != 6 || !tc.p.Cleanup || !tc.p.Verify || tc.p.Sweep {
			t.Errorf("%s must run 3+5 angles × 6 with verify and no sweep, got %+v", tc.name, tc.p)
		}
	}
	if !med.ConfirmedOnly || med.MaxFindings != 8 {
		t.Errorf("medium is the precision level (CONFIRMED only, ≤8), got %+v", med)
	}
	if high.ConfirmedOnly || high.MaxFindings != 10 {
		t.Errorf("high is recall-biased (PLAUSIBLE survives, ≤10), got %+v", high)
	}
	for _, tc := range []struct {
		name string
		p    EffortProfile
	}{{"xhigh", xhigh}, {"max", max}} {
		if len(tc.p.Angles) != 5 || tc.p.PerAngle != 8 || !tc.p.Cleanup || !tc.p.Verify ||
			!tc.p.Sweep || tc.p.ConfirmedOnly || tc.p.MaxFindings != 15 {
			t.Errorf("%s must run 5+5 angles × 8 with verify + sweep → ≤15, got %+v", tc.name, tc.p)
		}
	}
	// max differs from xhigh only in the API reasoning effort.
	if max.ReasoningEffort != "high" || xhigh.ReasoningEffort != "" {
		t.Errorf("only max sets the reasoning-effort knob: xhigh %q, max %q", xhigh.ReasoningEffort, max.ReasoningEffort)
	}
	if low.ReasoningEffort != "" || med.ReasoningEffort != "" || high.ReasoningEffort != "" {
		t.Error("levels below max must leave reasoning effort at the provider default")
	}
}

// TestEffortProfiles_PerAgentCaps pins the cost contract: high keeps every
// role agent under 120K tokens, and medium/low are strictly cheaper tiers.
func TestEffortProfiles_PerAgentCaps(t *testing.T) {
	low, med, high := profile(EffortLow), profile(EffortMedium), profile(EffortHigh)
	xhigh, max := profile(EffortXHigh), profile(EffortMax)

	if high.PerAgentTokens >= 120_000 {
		t.Fatalf("high per-agent cap %d must stay under 120K", high.PerAgentTokens)
	}
	if !(low.PerAgentTokens < med.PerAgentTokens && med.PerAgentTokens < high.PerAgentTokens &&
		high.PerAgentTokens <= xhigh.PerAgentTokens && xhigh.PerAgentTokens == max.PerAgentTokens) {
		t.Fatalf("per-agent caps must rise with level: low %d, medium %d, high %d, xhigh %d, max %d",
			low.PerAgentTokens, med.PerAgentTokens, high.PerAgentTokens, xhigh.PerAgentTokens, max.PerAgentTokens)
	}
	// Lower levels must not out-spend higher ones on any per-phase knob either.
	order := []EffortProfile{low, med, high, xhigh}
	for i := 1; i < len(order); i++ {
		lower, higher := phaseLimits(order[i-1]), phaseLimits(order[i])
		for name, ll := range lower {
			hl := higher[name]
			if ll.MaxTurns > hl.MaxTurns || ll.MaxToolCalls > hl.MaxToolCalls || ll.MaxContextTokens > hl.MaxContextTokens {
				t.Errorf("%s limits must be monotone across levels: %+v then %+v", name, ll, hl)
			}
		}
	}
}

// TestEffortProfiles_CapProvablyHolds checks the budget math phase by phase: a
// single-turn agent bills at most context + completion, and a multi-turn agent
// is gated with a two-turn reserve — either way the run's total stays under the
// level's per-agent cap.
func TestEffortProfiles_CapProvablyHolds(t *testing.T) {
	for _, e := range []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax} {
		p := profile(e)
		for name, l := range phaseLimits(p) {
			turnMax := l.MaxContextTokens + maxOutputTokens
			if l.MaxTurns == 1 {
				if turnMax >= p.PerAgentTokens {
					t.Errorf("%s/%s: single turn can bill %d ≥ cap %d", e, name, turnMax, p.PerAgentTokens)
				}
				continue
			}
			at := gateTokens(p.PerAgentTokens, l)
			if at <= 0 {
				t.Errorf("%s/%s: gate fires immediately (threshold %d) — limits leave no room to work", e, name, at)
			}
			if worst := at + 2*turnMax; worst > p.PerAgentTokens {
				t.Errorf("%s/%s: worst-case spend %d exceeds per-agent cap %d", e, name, worst, p.PerAgentTokens)
			}
		}
	}
}

func TestBudgetGate_PerRunCap(t *testing.T) {
	b := NewBudget(Config{MaxUSD: 100, MaxTokensTotal: 1_000_000})
	gate := b.Gate(100)
	if gate(nil, agentcore.Usage{InputTokens: 99}) {
		t.Fatal("run under its per-run cap must not fire")
	}
	if !gate(nil, agentcore.Usage{InputTokens: 60, CacheReadTokens: 40}) {
		t.Fatal("run at its per-run cap must fire (cache reads count)")
	}
	if !b.Gate(0)(nil, agentcore.Usage{}) {
		t.Fatal("a zero per-run cap fires immediately (degenerate one-turn run)")
	}
	if b.Gate(-1)(nil, agentcore.Usage{InputTokens: 500_000}) {
		t.Fatal("negative per-run cap means uncapped; shared caps not reached")
	}
}

func TestConfirmedOnly(t *testing.T) {
	in := []Finding{
		{Candidate: Candidate{File: "a.go"}, Verdict: VerdictConfirmed},
		{Candidate: Candidate{File: "b.go"}, Verdict: VerdictPlausible},
		{Candidate: Candidate{File: "c.go"}, Verdict: VerdictConfirmed},
	}
	out := confirmedOnly(in)
	if len(out) != 2 || out[0].File != "a.go" || out[1].File != "c.go" {
		t.Fatalf("precision filter must keep only CONFIRMED in order, got %+v", out)
	}
}

func TestUnverifiedFindings(t *testing.T) {
	out := unverifiedFindings([]Candidate{{File: "a.go", Line: 3}})
	if len(out) != 1 || out[0].Verdict != VerdictPlausible || out[0].File != "a.go" {
		t.Fatalf("low effort reports candidates as unverified PLAUSIBLE findings, got %+v", out)
	}
}

func TestLoadConfig_Effort(t *testing.T) {
	t.Setenv("SWATTER_API_KEY", "k")
	t.Setenv("SWATTER_MODEL", "m")

	t.Setenv("SWATTER_EFFORT", "")
	c, err := LoadConfig()
	if err != nil || c.Effort != EffortHigh {
		t.Fatalf("default effort = %q, err %v; want high", c.Effort, err)
	}

	for _, e := range []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax} {
		t.Setenv("SWATTER_EFFORT", string(e))
		if c, err = LoadConfig(); err != nil || c.Effort != e {
			t.Fatalf("effort %s = %q, err %v", e, c.Effort, err)
		}
	}

	t.Setenv("SWATTER_EFFORT", "turbo")
	if _, err = LoadConfig(); err == nil {
		t.Fatal("unknown effort must fail validation")
	}
}
