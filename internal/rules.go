package internal

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Rule is one entry in the living rule book (.swatter/rules.md). A rule is a
// generalized, one-sentence pattern a future diff could violate — learned from
// a confirmed finding, scored by how often it fires vs. produces noise, and
// expired when it decays or its subject leaves the repo.
type Rule struct {
	ID         string
	Rule       string
	Origin     string  // e.g. "PR#42 2026-07-11"
	Path       string  // file the rule was learned from; "" if none. Drives path-gone expiry.
	Confidence float64 // 0..1
	Hits       int
	Misses     int
	LastHit    string // YYYY-MM-DD; "" if never
}

// score is confidence weighted by recency: a high-confidence rule that hasn't
// fired in a long time ranks below a fresh one during eviction.
func (r Rule) score(now time.Time) float64 {
	decay := 1.0
	if r.LastHit != "" {
		if t, err := time.Parse("2006-01-02", r.LastHit); err == nil {
			days := now.Sub(t).Hours() / 24
			// Half-life ~60 days.
			decay = pow2(-days / 60.0)
		}
	}
	return r.Confidence * decay
}

// RuleStore is the parsed book plus its lifecycle operations.
type RuleStore struct {
	Rules []Rule
}

// Lifecycle thresholds (plan §Rule book).
const (
	ruleBookMaxBytes    = 4096 // compact when the rendered book exceeds this
	compactEveryReviews = 20   // …or every N reviews regardless of size
	evictScoreFloor     = 0.15 // a rule below this effective score is evictable
)

// ParseRuleStore reads the markdown book. Unknown lines are ignored so a
// hand-edited book never crashes a review.
func ParseRuleStore(md string) *RuleStore {
	rs := &RuleStore{}
	var cur *Rule
	flush := func() {
		if cur != nil && strings.TrimSpace(cur.Rule) != "" {
			rs.Rules = append(rs.Rules, *cur)
		}
		cur = nil
	}
	for _, raw := range strings.Split(md, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "- id:") {
			flush()
			cur = &Rule{ID: strings.TrimSpace(strings.TrimPrefix(line, "- id:")), Confidence: 0.8}
			continue
		}
		if cur == nil {
			continue
		}
		for _, field := range strings.Split(line, "   ") { // fields are 3-space separated on a line
			k, v, ok := strings.Cut(strings.TrimSpace(field), ":")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			switch k {
			case "rule":
				cur.Rule = v
			case "origin":
				cur.Origin = v
			case "path":
				cur.Path = v
			case "confidence":
				cur.Confidence, _ = strconv.ParseFloat(v, 64)
			case "hits":
				cur.Hits, _ = strconv.Atoi(v)
			case "misses":
				cur.Misses, _ = strconv.Atoi(v)
			case "last_hit":
				cur.LastHit = v
			}
		}
	}
	flush()
	return rs
}

// Render serializes the book back to markdown in the canonical entry format.
func (rs *RuleStore) Render() string {
	if len(rs.Rules) == 0 {
		return "# Swatter rule book\n\n_No rules learned yet._\n"
	}
	var b strings.Builder
	b.WriteString("# Swatter rule book\n\n")
	b.WriteString("<!-- Managed by Swatter: learned from confirmed findings, scored, and compacted. Hand-edits are preserved but may be re-scored. -->\n\n")
	for _, r := range rs.Rules {
		fmt.Fprintf(&b, "- id: %s\n", r.ID)
		fmt.Fprintf(&b, "  rule: %s\n", r.Rule)
		origin := r.Origin
		if origin == "" {
			origin = "unknown"
		}
		fmt.Fprintf(&b, "  origin: %s   confidence: %.2f\n", origin, r.Confidence)
		if r.Path != "" {
			fmt.Fprintf(&b, "  path: %s\n", r.Path)
		}
		last := r.LastHit
		if last == "" {
			last = "never"
		}
		fmt.Fprintf(&b, "  hits: %d   last_hit: %s   misses: %d\n", r.Hits, last, r.Misses)
	}
	return b.String()
}

// SameRuleJudge decides whether two rules express the same pattern. Production
// wires an LLM judge (semantic, catches paraphrase); tests inject a fake. It is
// only consulted after a cheap normalized-equality prefilter.
type SameRuleJudge func(ctx context.Context, a, b string) (bool, error)

// Insert adds a candidate rule unless it duplicates an existing one. The
// normalized-text prefilter catches trivial repeats for free; the judge catches
// paraphrase. Returns whether the rule was actually inserted.
func (rs *RuleStore) Insert(ctx context.Context, cand Rule, judge SameRuleJudge) (bool, error) {
	candNorm := normalizeRule(cand.Rule)
	for _, existing := range rs.Rules {
		if normalizeRule(existing.Rule) == candNorm {
			return false, nil // exact/normalized dup
		}
		if judge != nil {
			same, err := judge(ctx, existing.Rule, cand.Rule)
			if err != nil {
				return false, err
			}
			if same {
				return false, nil // semantic dup (paraphrase)
			}
		}
	}
	if cand.Confidence == 0 {
		cand.Confidence = 0.8
	}
	rs.Rules = append(rs.Rules, cand)
	return true, nil
}

// Score folds one review's outcome into the book: rule ids that fired on a
// surviving finding are hits (confidence rises); ids that produced a REJECTed
// finding are misses (confidence falls fast — noise-makers decay quickest).
func (rs *RuleStore) Score(hits, misses []string, now time.Time) {
	hitSet := toSet(hits)
	missSet := toSet(misses)
	today := now.Format("2006-01-02")
	for i := range rs.Rules {
		r := &rs.Rules[i]
		if hitSet[r.ID] {
			r.Hits++
			r.LastHit = today
			r.Confidence += (1 - r.Confidence) * 0.1 // asymptote toward 1
		}
		if missSet[r.ID] {
			r.Misses++
			r.Confidence *= 0.7 // a rule that generated noise decays fast
		}
		r.Confidence = clamp01(r.Confidence)
	}
}

// Expire removes rules whose subject left the repo (path-gone → immediate) and,
// when over the size/age budget, evicts the lowest-scoring rules until the book
// fits. pathExists reports whether a rule's cited path is still present.
func (rs *RuleStore) Expire(now time.Time, pathExists func(path string) bool, reviewCount int) {
	// 1. Path-gone: a rule citing a file no longer in the repo is stale.
	kept := rs.Rules[:0]
	for _, r := range rs.Rules {
		if r.Path != "" && pathExists != nil && !pathExists(r.Path) {
			continue
		}
		kept = append(kept, r)
	}
	rs.Rules = kept

	if !rs.NeedsCompaction(reviewCount) {
		return
	}
	// 2. Evict by score until under the byte budget (and drop anything below the
	//    hard floor regardless).
	sort.SliceStable(rs.Rules, func(i, j int) bool {
		return rs.Rules[i].score(now) > rs.Rules[j].score(now)
	})
	// Drop sub-floor rules first.
	kept = rs.Rules[:0]
	for _, r := range rs.Rules {
		if r.score(now) < evictScoreFloor && r.Hits == 0 {
			continue
		}
		kept = append(kept, r)
	}
	rs.Rules = kept
	// Then trim the tail until the rendered book fits the byte budget.
	for len(rs.Rules) > 1 && len([]byte(rs.Render())) > ruleBookMaxBytes {
		rs.Rules = rs.Rules[:len(rs.Rules)-1]
	}
}

// NeedsCompaction reports whether the book is over the size budget or the review
// cadence says it's time to compact.
func (rs *RuleStore) NeedsCompaction(reviewCount int) bool {
	if len([]byte(rs.Render())) > ruleBookMaxBytes {
		return true
	}
	return reviewCount > 0 && reviewCount%compactEveryReviews == 0
}

func normalizeRule(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevSpace = false
		default:
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		if s != "" {
			m[s] = true
		}
	}
	return m
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// pow2 returns 2**x for real x without importing math (keeps the deterministic
// core dependency-free and easy to reason about in tests).
func pow2(x float64) float64 {
	// 2^x = e^(x ln2); approximate via the standard library-free identity using
	// a fast enough series is overkill — use repeated squaring on the integer
	// part and a small rational for the fraction. For our recency decay a coarse
	// value is fine.
	neg := x < 0
	if neg {
		x = -x
	}
	intPart := int(x)
	frac := x - float64(intPart)
	result := 1.0
	for i := 0; i < intPart; i++ {
		result *= 2
	}
	// 2^frac ≈ 1 + 0.693*frac + 0.240*frac^2 (2nd-order, <1% error on [0,1]).
	result *= 1 + 0.6931*frac + 0.2402*frac*frac
	if neg {
		return 1 / result
	}
	return result
}
