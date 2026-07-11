package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lohi-ai/agentray/agentcore"
)

// learnedRule is the learn agent's output shape.
type learnedRule struct {
	Rule       string  `json:"rule"`
	Confidence float64 `json:"confidence"`
}

// LearnRules runs the learn pass: given the confirmed findings and the existing
// book, it proposes generalized rules and inserts the non-duplicates (dedup via
// the LLM SameRuleJudge). It mutates store in place. Returns the number of rules
// added. No-op when there are no confirmed findings.
func (d *runnerDeps) LearnRules(ctx context.Context, packet *Packet, confirmed []Finding, store *RuleStore) (int, error) {
	if len(confirmed) == 0 {
		return 0, nil
	}
	fj, _ := json.Marshal(confirmed)
	limits := agentcore.Limits{MaxTurns: 3, MaxToolCalls: 2, MaxToolResultLen: 8_000, MaxContextTokens: 60_000}
	ag, err := d.roleAgent(d.cfg.ModelStrong, LearnPrompt(), "", limits)
	if err != nil {
		return 0, err
	}
	input := fmt.Sprintf("## Confirmed findings this review\n```json\n%s\n```\n\n## Existing rule book\n%s\n\nReturn the JSON array of new generalized rules.",
		string(fj), fallback(store.Render(), "(empty)"))
	ctx = agentcore.WithTraceID(ctx, "learn")
	r, err := d.run(ctx, ag, input)
	if err != nil {
		return 0, err
	}
	cands := parseLearnedRules(r.Final)

	judge := d.sameRuleJudge()
	origin := packet.HeadRef
	if origin == "" {
		origin = "review"
	}
	date := time.Now().Format("2006-01-02")
	added := 0
	for i, lr := range cands {
		if strings.TrimSpace(lr.Rule) == "" {
			continue
		}
		rule := Rule{
			ID:         fmt.Sprintf("r-%s-%d", date, len(store.Rules)+i+1),
			Rule:       strings.TrimSpace(lr.Rule),
			Origin:     fmt.Sprintf("%s %s", origin, date),
			Confidence: clamp01(orDefault(lr.Confidence, 0.8)),
			Path:       originPath(confirmed),
		}
		ok, err := store.Insert(ctx, rule, judge)
		if err != nil {
			return added, err
		}
		if ok {
			added++
		}
	}
	return added, nil
}

// sameRuleJudge returns an LLM-backed SameRuleJudge: a tiny yes/no agent that
// decides whether two rules express the same pattern (catches paraphrase the
// normalized prefilter misses — the Litrans-bible near-dup lesson).
func (d *runnerDeps) sameRuleJudge() SameRuleJudge {
	return func(ctx context.Context, a, b string) (bool, error) {
		limits := agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 2_000, MaxContextTokens: 8_000}
		soul := "You judge whether two code-review rules express the SAME underlying pattern (one a paraphrase, generalization, or subset of the other). Answer with a single word: YES or NO."
		ag, err := d.roleAgent(d.cfg.ModelCheap, soul, "", limits)
		if err != nil {
			return false, err
		}
		input := fmt.Sprintf("Rule A: %s\nRule B: %s\n\nSame pattern? Answer YES or NO.", a, b)
		ctx = agentcore.WithTraceID(ctx, "dedup")
		r, err := d.run(ctx, ag, input)
		if err != nil {
			return false, err
		}
		return strings.Contains(strings.ToUpper(r.Final), "YES"), nil
	}
}

func parseLearnedRules(raw string) []learnedRule {
	body := extractJSONArray(raw)
	if body == "" {
		return nil
	}
	var out []learnedRule
	_ = json.Unmarshal([]byte(body), &out)
	return out
}

// originPath returns the file of the first confirmed finding, used to anchor a
// learned rule for path-gone expiry. "" when no clear single origin.
func originPath(confirmed []Finding) string {
	if len(confirmed) == 1 {
		return confirmed[0].File
	}
	return "" // multiple origins: don't tie the rule to one path
}

func fallback(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func orDefault(f, def float64) float64 {
	if f == 0 {
		return def
	}
	return f
}
