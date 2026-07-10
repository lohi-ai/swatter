package internal

import (
	"testing"

	"github.com/lohi-ai/agentray/agentcore"
)

func TestPackAngles_BySize(t *testing.T) {
	// Large diff: every angle rides alone.
	if got := packAngles(2000); len(got) != len(AllAngles) {
		t.Fatalf("large: want %d runs, got %d", len(AllAngles), len(got))
	}
	// Tiny diff: packed into 3 runs.
	if got := packAngles(50); len(got) != 3 {
		t.Fatalf("tiny: want 3 runs, got %d", len(got))
	}
	// Normal: 6 runs, and every angle A–H appears exactly once across runs.
	got := packAngles(400)
	seen := map[string]int{}
	for _, g := range got {
		for _, a := range g {
			seen[a]++
		}
	}
	for _, a := range AllAngles {
		if seen[a] != 1 {
			t.Fatalf("normal: angle %s appears %d times, want 1", a, seen[a])
		}
	}
}

func TestIsCheapEligible(t *testing.T) {
	if isCheapEligible([]string{"A"}, 400) {
		t.Fatal("bug angle A must never be cheap")
	}
	if isCheapEligible([]string{"E", "H"}, 400) {
		t.Fatal("group containing H (guard sibling) must not be cheap")
	}
	if !isCheapEligible([]string{"F", "G"}, 400) {
		t.Fatal("F,G on a small diff should be cheap-eligible")
	}
	if isCheapEligible([]string{"F", "G"}, 2000) {
		t.Fatal("large diff must never be cheap")
	}
}

func TestBudgetLedger_TokenBackstopFires(t *testing.T) {
	// Unknown gateway model prices $0; the token backstop must still fire.
	cfg := Config{MaxUSD: 100, MaxTokensTotal: 1000}
	b := NewBudget(cfg)
	gate := b.Gate()
	if gate(nil, agentcore.Usage{InputTokens: 500}) {
		t.Fatal("should not fire under cap")
	}
	b.Commit(agentcore.Usage{InputTokens: 900})
	if !gate(nil, agentcore.Usage{InputTokens: 200}) {
		t.Fatal("committed 900 + live 200 >= 1000 must fire")
	}
	if !b.Exhausted() == false {
		// committed alone is 900 < 1000, not yet exhausted
	}
	b.Commit(agentcore.Usage{InputTokens: 200})
	if !b.Exhausted() {
		t.Fatal("committed 1100 >= 1000 must be exhausted")
	}
}

func TestBudgetLedger_PriceOverrideMeters(t *testing.T) {
	cfg := Config{MaxUSD: 1.0, MaxTokensTotal: 0, PricePerMTokIn: 3.0, PricePerMTokOut: 15.0}
	b := NewBudget(cfg)
	gate := b.Gate()
	// 200k in + 40k out = 0.6 + 0.6 = $1.2 > $1.0 → fire, even though agentcore
	// priced the usage at $0 (unknown model).
	u := agentcore.Usage{InputTokens: 200_000, OutputTokens: 40_000, CostUSD: 0}
	if !gate(nil, u) {
		t.Fatal("price override should make MaxUSD fire on an unknown-priced model")
	}
}

func TestConfigFails(t *testing.T) {
	maj := Config{FailOn: FailOnMajor}
	if !maj.Fails(SevCritical) || !maj.Fails(SevMajor) || maj.Fails(SevMinor) {
		t.Fatal("major gate wrong")
	}
	crit := Config{FailOn: FailOnCritical}
	if crit.Fails(SevMajor) || !crit.Fails(SevCritical) {
		t.Fatal("critical gate wrong")
	}
	if (Config{FailOn: FailOnNever}).Fails(SevCritical) {
		t.Fatal("never must not fail")
	}
	if !(Config{FailOn: FailOnAny}).Fails(SevMinor) {
		t.Fatal("any must fail on minor")
	}
}
