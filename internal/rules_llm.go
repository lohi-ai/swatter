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
	ag, err := d.roleAgent(d.cfg.ModelStrong, LearnPrompt(), "", d.cfg.EffortProfile().Limits.Learn)
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

// sameRuleJudge returns an LLM-backed SameRuleJudge: a tiny agent that checks
// the candidate against the whole book in one call and answers the matching
// rule's number, or NONE (catches paraphrase the normalized prefilter misses —
// the Litrans-bible near-dup lesson). One call per candidate instead of one
// per (candidate, rule) pair; the book is ≤4KB, well inside the judge budget.
func (d *runnerDeps) sameRuleJudge() SameRuleJudge {
	return func(ctx context.Context, cand string, existing []string) (int, error) {
		soul := "You judge whether a CANDIDATE code-review rule expresses the SAME underlying pattern as any rule in a numbered list (one a paraphrase, generalization, or subset of the other). Answer with the matching rule's number alone, or the single word NONE."
		ag, err := d.roleAgent(d.cfg.ModelCheap, soul, "", d.cfg.EffortProfile().Limits.Judge)
		if err != nil {
			return -1, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Candidate rule: %s\n\nExisting rules:\n", cand)
		for i, r := range existing {
			fmt.Fprintf(&b, "%d. %s\n", i+1, r)
		}
		b.WriteString("\nSame pattern as any existing rule? Answer its number, or NONE.")
		ctx = agentcore.WithTraceID(ctx, "dedup")
		r, err := d.run(ctx, ag, b.String())
		if err != nil {
			return -1, err
		}
		return parseJudgeMatch(r.Final, len(existing)), nil
	}
}

// parseJudgeMatch reads the judge's reply: the first integer in [1, n] wins
// (0-indexed on return); anything else — NONE, prose, an out-of-range number —
// means no match. A malformed reply therefore fails open to "insert", the same
// bias as the old YES/NO judge's substring check.
func parseJudgeMatch(reply string, n int) int {
	num, inNum := 0, false
	flush := func() int {
		if inNum && num >= 1 && num <= n {
			return num - 1
		}
		return -1
	}
	for _, r := range reply {
		if r >= '0' && r <= '9' {
			num = num*10 + int(r-'0')
			inNum = true
			continue
		}
		if m := flush(); m >= 0 {
			return m
		}
		num, inNum = 0, false
	}
	return flush()
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
