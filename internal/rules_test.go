package internal

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRuleStore_ParseRenderRoundTrip(t *testing.T) {
	md := `# Swatter rule book

- id: r-2026-07-11-1
  rule: Wrap external API calls in the shared withRetry helper
  origin: PR#42 2026-07-11   confidence: 0.90
  path: api/client.go
  hits: 3   last_hit: 2026-07-10   misses: 1
`
	rs := ParseRuleStore(md)
	if len(rs.Rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(rs.Rules))
	}
	r := rs.Rules[0]
	if r.ID != "r-2026-07-11-1" || !strings.Contains(r.Rule, "withRetry") ||
		r.Confidence != 0.9 || r.Hits != 3 || r.Misses != 1 ||
		r.Path != "api/client.go" || r.LastHit != "2026-07-10" {
		t.Fatalf("round-trip lost data: %+v", r)
	}
	// Re-parse the render to confirm stability.
	rs2 := ParseRuleStore(rs.Render())
	if len(rs2.Rules) != 1 || rs2.Rules[0].ID != r.ID || rs2.Rules[0].Confidence != r.Confidence {
		t.Fatalf("render→parse not stable: %+v", rs2.Rules)
	}
}

func TestRuleStore_InsertDedupsParaphrase(t *testing.T) {
	rs := &RuleStore{Rules: []Rule{
		{ID: "r-1", Rule: "Always validate user input before the DB query", Confidence: 0.9},
	}}
	// A fake semantic judge that treats anything mentioning "validate" + "input"
	// as the same rule — stands in for the LLM paraphrase catcher.
	judge := func(_ context.Context, a, b string) (bool, error) {
		// Same rule iff BOTH texts are about validating input — a stand-in for
		// the LLM catching paraphrase while leaving unrelated rules distinct.
		return aboutValidation(a) && aboutValidation(b), nil
	}
	// Exact-normalized dup: rejected without the judge.
	added, _ := rs.Insert(context.Background(), Rule{Rule: "always validate USER input before the db query"}, nil)
	if added {
		t.Fatal("normalized duplicate should not be inserted")
	}
	// Paraphrase: different words, same meaning → judge rejects it.
	added, _ = rs.Insert(context.Background(),
		Rule{Rule: "Sanitize and validate every request input prior to querying"}, judge)
	if added {
		t.Fatal("paraphrase should be deduped by the semantic judge")
	}
	// Genuinely new rule: inserted.
	added, _ = rs.Insert(context.Background(),
		Rule{Rule: "Use parameterized queries, never string concatenation"}, judge)
	if !added {
		t.Fatal("a distinct rule should be inserted")
	}
	if len(rs.Rules) != 2 {
		t.Fatalf("want 2 rules after dedup, got %d", len(rs.Rules))
	}
}

func TestRuleStore_Score(t *testing.T) {
	rs := &RuleStore{Rules: []Rule{
		{ID: "r-hit", Rule: "a", Confidence: 0.5},
		{ID: "r-miss", Rule: "b", Confidence: 0.8},
	}}
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	rs.Score([]string{"r-hit"}, []string{"r-miss"}, now)
	if rs.Rules[0].Hits != 1 || rs.Rules[0].Confidence <= 0.5 || rs.Rules[0].LastHit != "2026-07-11" {
		t.Fatalf("hit not scored: %+v", rs.Rules[0])
	}
	if rs.Rules[1].Misses != 1 || rs.Rules[1].Confidence >= 0.8 {
		t.Fatalf("miss not scored: %+v", rs.Rules[1])
	}
}

func TestRuleStore_ScoredPRsIdempotent(t *testing.T) {
	rs := &RuleStore{Rules: []Rule{{ID: "r-x", Rule: "a", Confidence: 0.5}}}
	if !rs.MarkScored(7) {
		t.Fatal("first MarkScored should report the PR as newly scored")
	}
	if rs.MarkScored(7) {
		t.Fatal("second MarkScored for the same PR must report already-scored")
	}
	// The guard must survive a render/parse round-trip, so a re-run in a fresh
	// CI process still sees the PR as scored and won't double-count its feedback.
	rs2 := ParseRuleStore(rs.Render())
	if !rs2.HasScored(7) {
		t.Fatalf("scored-PR marker lost across render/parse: %q", rs.Render())
	}
	if rs2.MarkScored(7) {
		t.Fatal("re-parsed store must still treat PR 7 as scored")
	}
}

func TestRuleStore_ExpirePathGone(t *testing.T) {
	rs := &RuleStore{Rules: []Rule{
		{ID: "r-keep", Rule: "a", Confidence: 0.9, Path: "kept.go"},
		{ID: "r-gone", Rule: "b", Confidence: 0.9, Path: "deleted.go"},
		{ID: "r-nopath", Rule: "c", Confidence: 0.9},
	}}
	pathExists := func(p string) bool { return p == "kept.go" }
	rs.Expire(time.Now(), pathExists, 0) // reviewCount 0 → no size compaction
	ids := ruleIDs(rs)
	if _, gone := ids["r-gone"]; gone {
		t.Fatal("rule citing a deleted path must be expired")
	}
	if _, ok := ids["r-keep"]; !ok {
		t.Fatal("rule citing an existing path must be kept")
	}
	if _, ok := ids["r-nopath"]; !ok {
		t.Fatal("rule with no path must be kept")
	}
}

func TestRuleStore_CompactEvictsByScore(t *testing.T) {
	// Build a book over the byte budget with a mix of strong and weak rules.
	var rules []Rule
	for i := 0; i < 40; i++ {
		conf := 0.9
		last := "2026-07-10"
		if i%2 == 1 {
			conf = 0.1 // weak, never-hit rules should be evicted first
			last = "2020-01-01"
		}
		rules = append(rules, Rule{
			ID:         "r-" + string(rune('a'+i%26)) + itoa(i),
			Rule:       "rule number " + itoa(i) + " with enough text to take up bytes in the book so we cross the budget",
			Confidence: conf, LastHit: last, Hits: 0,
		})
	}
	rs := &RuleStore{Rules: rules}
	if !rs.NeedsCompaction(0) {
		t.Skip("book not over budget in this build; nothing to assert")
	}
	before := len(rs.Rules)
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	rs.Expire(now, nil, 20) // reviewCount 20 → compaction cadence fires
	if len(rs.Rules) >= before {
		t.Fatalf("compaction should have evicted rules: before=%d after=%d", before, len(rs.Rules))
	}
	if got := len([]byte(rs.Render())); got > ruleBookMaxBytes {
		t.Fatalf("book still over budget after compaction: %d bytes", got)
	}
	// The highest-scoring rule must survive.
	if _, ok := ruleIDs(rs)["r-a0"]; !ok {
		t.Fatalf("strongest rule evicted; survivors: %v", ruleIDs(rs))
	}
}

func aboutValidation(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "validate") && (strings.Contains(low, "input") || strings.Contains(low, "request"))
}

func ruleIDs(rs *RuleStore) map[string]bool {
	m := map[string]bool{}
	for _, r := range rs.Rules {
		m[r.ID] = true
	}
	return m
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
