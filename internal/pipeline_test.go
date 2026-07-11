package internal

import (
	"context"
	"strings"
	"testing"

	"github.com/lohi-ai/agentray/agentcore"
)

// shortVerdictProvider always returns a one-element verdict array, regardless of
// how many candidates it was asked to judge — the exact shape that used to make
// verifyGroup silently drop the unjudged remainder.
type shortVerdictProvider struct{}

func (shortVerdictProvider) Name() string        { return "short" }
func (shortVerdictProvider) SupportsTools() bool { return true }

func (shortVerdictProvider) Chat(_ context.Context, _ agentcore.ChatRequest) (agentcore.ChatResponse, error) {
	return agentcore.AssistantText(`[{"verdict":"CONFIRMED","severity":"MAJOR","rationale":"only the first"}]`), nil
}

func (shortVerdictProvider) Stream(_ context.Context, _ agentcore.ChatRequest) (<-chan agentcore.ChatDelta, error) {
	ch := make(chan agentcore.ChatDelta, 2)
	go func() {
		defer close(ch)
		ch <- agentcore.ChatDelta{ContentDelta: `[{"verdict":"CONFIRMED","severity":"MAJOR","rationale":"only the first"}]`}
		ch <- agentcore.ChatDelta{Done: true, StopReason: "stop"}
	}()
	return ch, nil
}

// TestVerifyGroup_PartialVerdictsPassThrough guards the recall fix: when a
// verifier returns fewer verdicts than the candidates it was handed, the
// unjudged ones must survive as unvalidated PLAUSIBLE, not be dropped.
func TestVerifyGroup_PartialVerdictsPassThrough(t *testing.T) {
	cfg := Config{Provider: ProviderAnthropic, APIKey: "stub", ModelStrong: "s", ModelCheap: "c",
		MaxUSD: 1000, MaxTokensTotal: 1 << 40, FailOn: FailOnNever, RepoRoot: "."}
	deps, err := newRunnerDeps(cfg, NewBudget(cfg))
	if err != nil {
		t.Fatalf("deps: %v", err)
	}
	deps.inner = shortVerdictProvider{}
	p := &Pipeline{cfg: cfg, deps: deps, packet: &Packet{}, progress: func(string) {}}

	group := []Candidate{
		{File: "a.go", Line: 5, Angle: "A", Summary: "first", Severity: SevMajor},
		{File: "a.go", Line: 5, Angle: "C", Summary: "second", Severity: SevMajor},
	}
	findings, validated, rejected, _ := p.verifyGroup(context.Background(), "brief", group)
	if len(findings) != 2 {
		t.Fatalf("want 2 findings (1 judged + 1 passthrough), got %d: %+v", len(findings), findings)
	}
	if validated != 1 || rejected != 0 {
		t.Fatalf("want validated=1 rejected=0, got validated=%d rejected=%d", validated, rejected)
	}
	if findings[0].Verdict != VerdictConfirmed {
		t.Errorf("judged candidate should be CONFIRMED, got %s", findings[0].Verdict)
	}
	if findings[1].Verdict != VerdictPlausible || !strings.Contains(findings[1].Rationale, "did not rule") {
		t.Errorf("unjudged candidate must pass through as PLAUSIBLE, got %s / %q", findings[1].Verdict, findings[1].Rationale)
	}
}

func TestGroupByLocation(t *testing.T) {
	cands := []Candidate{
		{File: "a.go", Line: 5, Angle: "A"},
		{File: "a.go", Line: 5, Angle: "C"}, // same location → one group
		{File: "a.go", Line: 9, Angle: "B"},
		{File: "b.go", Line: 0, Angle: "cleanup"}, // file-level
	}
	groups := groupByLocation(cands)
	if len(groups) != 3 {
		t.Fatalf("want 3 location groups, got %d: %+v", len(groups), groups)
	}
	if len(groups[0]) != 2 {
		t.Fatalf("a.go:5 should pool 2 candidates, got %d", len(groups[0]))
	}
}

func TestCountConsensus(t *testing.T) {
	cands := []Candidate{
		{File: "a.go", Line: 5, Angle: "A"},
		{File: "a.go", Line: 5, Angle: "C"}, // 2 distinct angles → consensus
		{File: "a.go", Line: 9, Angle: "B"}, // lone
		{File: "b.go", Line: 1, Angle: "D"},
		{File: "b.go", Line: 1, Angle: "D"}, // same angle twice → not consensus
	}
	if got := countConsensus(cands); got != 1 {
		t.Fatalf("want 1 consensus location, got %d", got)
	}
}

func TestCanonicalizePath(t *testing.T) {
	files := []string{"internal/store.go", "cmd/main.go"}
	cases := map[string]string{
		"internal/store.go": "internal/store.go", // exact
		"store.go":          "internal/store.go", // suffix match
		"restore.go":        "restore.go",        // NOT a segment suffix → unchanged
		"other.go":          "other.go",          // no match → unchanged
	}
	for in, want := range cases {
		if got := canonicalizePath(in, files); got != want {
			t.Errorf("canonicalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplySynthesis_MergesByIndex(t *testing.T) {
	ranked := []Finding{
		{Candidate: Candidate{File: "a.go", Line: 5, Angle: "A", Severity: SevMajor}, Verdict: VerdictPlausible},
		{Candidate: Candidate{File: "b.go", Line: 1, Angle: "B", Severity: SevMinor}, Verdict: VerdictConfirmed},
		{Candidate: Candidate{File: "a.go", Line: 6, Angle: "C", Severity: SevCritical}, Verdict: VerdictConfirmed},
	}
	// Merge index 2 (critical, confirmed) into primary 0.
	out := applySynthesis(`{"findings":[{"primary":0,"merge":[2]},{"primary":1}]}`, ranked)
	if len(out) != 2 {
		t.Fatalf("want 2 findings, got %d", len(out))
	}
	if out[0].Severity != SevCritical {
		t.Errorf("merged severity = %s, want CRITICAL", out[0].Severity)
	}
	if out[0].Verdict != VerdictConfirmed {
		t.Errorf("merged verdict = %s, want CONFIRMED (escalated)", out[0].Verdict)
	}
	if out[0].Angle != "A,C" {
		t.Errorf("merged angle = %q, want A,C", out[0].Angle)
	}
}

func TestApplySynthesis_Unparseable(t *testing.T) {
	if got := applySynthesis("not json", []Finding{{}}); got != nil {
		t.Fatalf("unparseable synthesis must return nil for fallback, got %+v", got)
	}
}

// TestApplySynthesis_OmittedIndicesSurvive guards the recall fix from the PR
// review: a synthesis reply that decodes but mentions only some indices must
// not drop the rest — they are appended in rank order.
func TestApplySynthesis_OmittedIndicesSurvive(t *testing.T) {
	ranked := []Finding{
		{Candidate: Candidate{File: "a.go", Line: 1, Summary: "one"}, Verdict: VerdictConfirmed},
		{Candidate: Candidate{File: "b.go", Line: 2, Summary: "two"}, Verdict: VerdictConfirmed},
		{Candidate: Candidate{File: "c.go", Line: 3, Summary: "three"}, Verdict: VerdictPlausible},
	}
	out := applySynthesis(`{"findings":[{"primary":0}]}`, ranked)
	if len(out) != 3 {
		t.Fatalf("want all 3 findings (1 mentioned + 2 appended), got %d: %+v", len(out), out)
	}
	if out[1].Summary != "two" || out[2].Summary != "three" {
		t.Fatalf("omitted findings must survive in rank order, got %+v", out)
	}
}

func TestBudgetLedger_TokenBackstopFires(t *testing.T) {
	// Unknown gateway model prices $0; the token backstop must still fire.
	cfg := Config{MaxUSD: 100, MaxTokensTotal: 1000}
	b := NewBudget(cfg)
	gate := b.Gate(-1)
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
	gate := b.Gate(-1)
	// 200k in + 40k out = 0.6 + 0.6 = $1.2 > $1.0 → fire, even though agentcore
	// priced the usage at $0 (unknown model).
	u := agentcore.Usage{InputTokens: 200_000, OutputTokens: 40_000, CostUSD: 0}
	if !gate(nil, u) {
		t.Fatal("price override should make MaxUSD fire on an unknown-priced model")
	}
}

func TestIsTruncated(t *testing.T) {
	// The cap states that leave Final without a final answer — a wrap-up turn
	// can salvage these.
	for _, s := range []string{"max_turns", "max_tool_calls"} {
		if !isTruncated(s) {
			t.Errorf("isTruncated(%q) = false, want true", s)
		}
	}
	// budget_exhausted is deliberately NOT truncated: the wrap-up is a fresh LLM
	// call the spent ledger cannot fund, and agentcore already summarizes on it.
	// Clean, budget, or errored stops must not trigger a wrap-up.
	for _, s := range []string{"stop", "", "end_turn", "budget_exhausted", "error", "aborted", "halted"} {
		if isTruncated(s) {
			t.Errorf("isTruncated(%q) = true, want false", s)
		}
	}
}

func TestNormalizeCandidateSeverities(t *testing.T) {
	got := normalizeCandidateSeverities([]Candidate{
		{},                     // missing → MINOR
		{Severity: "critical"}, // lower-case → canonical CRITICAL
		{Severity: SevMajor},   // already canonical
	})
	if got[0].Severity != SevMinor {
		t.Errorf("missing severity → %q, want %q", got[0].Severity, SevMinor)
	}
	if got[1].Severity != SevCritical {
		t.Errorf("\"critical\" → %q, want %q", got[1].Severity, SevCritical)
	}
	if got[2].Severity != SevMajor {
		t.Errorf("canonical MAJOR → %q, want %q", got[2].Severity, SevMajor)
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
