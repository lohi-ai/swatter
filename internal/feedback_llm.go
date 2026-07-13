package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// clusterProposal is the clustering agent's output shape: one proposed rule
// plus the observation ids it claims as evidence. The harness re-verifies the
// evidence against the ledger — the model proposes, it never decides.
type clusterProposal struct {
	Rule       string   `json:"rule"`
	Confidence float64  `json:"confidence"`
	MemberIDs  []string `json:"member_ids"`
}

// Feedback-promoted rules start below learned-from-CONFIRMED rules (0.8):
// the evidence is indirect (reactions, replies), so they must earn confidence
// through hits like any other rule.
const promotedRuleConfidence = 0.7

// clusterObservations runs the one clustering call over the pending ledger.
func (d *runnerDeps) clusterObservations(ctx context.Context, ledger *ObsLedger, store *RuleStore) ([]clusterProposal, error) {
	if len(ledger.Obs) == 0 {
		return nil, nil
	}
	type obsWire struct {
		ID   string  `json:"id"`
		Kind ObsKind `json:"kind"`
		PR   int     `json:"pr"`
		Path string  `json:"path,omitempty"`
		Note string  `json:"note"`
	}
	wire := make([]obsWire, 0, len(ledger.Obs))
	for _, o := range ledger.Obs {
		wire = append(wire, obsWire{ID: o.ID, Kind: o.Kind, PR: o.PR, Path: o.Path, Note: o.Note})
	}
	oj, _ := json.Marshal(wire)

	ag, err := d.roleAgent(d.cfg.ModelStrong, FeedbackClusterPrompt(), "", d.cfg.EffortProfile().Limits.Learn)
	if err != nil {
		return nil, err
	}
	input := fmt.Sprintf("## Pending observations\n```json\n%s\n```\n\n## Existing rule book\n%s\n\nReturn the JSON array of clusters.",
		string(oj), fallback(store.Render(), "(empty)"))
	r, err := d.run(ctx, ag, input)
	if err != nil {
		return nil, err
	}
	body := extractJSONArray(r.Final)
	if body == "" {
		return nil, nil
	}
	var out []clusterProposal
	_ = json.Unmarshal([]byte(body), &out)
	return out, nil
}

// PromoteObservations turns accumulated evidence into rules, conservatively:
// a proposed cluster becomes a rule only when its *verified* members carry
// weight ≥ promoteAfter (missed=2, repeat=1) AND span ≥ 2 distinct PRs — one
// noisy PR can never mint a rule on its own. Promoted (or already-covered)
// members leave the ledger; everything else stays and keeps accumulating.
// Returns the number of rules inserted. Mutates ledger and store in place.
//
// The clustering call only runs when the ledger as a whole could clear the
// gate (PromotionPossible) — a thin ledger can't mint a rule no matter how the
// model groups it, so the tokens would buy nothing.
func (d *runnerDeps) PromoteObservations(ctx context.Context, ledger *ObsLedger, store *RuleStore, promoteAfter int) (int, error) {
	if !ledger.PromotionPossible(promoteAfter) {
		return 0, nil
	}
	proposals, err := d.clusterObservations(ctx, ledger, store)
	if err != nil {
		return 0, err
	}
	judge := d.sameRuleJudge()
	date := time.Now().Format("2006-01-02")
	added := 0
	for _, p := range proposals {
		if strings.TrimSpace(p.Rule) == "" {
			continue
		}
		weight, distinctPRs, valid := ledger.ClusterEvidence(p.MemberIDs)
		if weight < promoteAfter || distinctPRs < 2 {
			continue // not enough evidence yet — observations stay pending
		}
		rule := Rule{
			ID:         fmt.Sprintf("r-%s-%d", date, len(store.Rules)+added+1),
			Rule:       strings.TrimSpace(p.Rule),
			Origin:     fmt.Sprintf("feedback %s (%d obs)", date, len(valid)),
			Confidence: clamp01(minF(orDefault(p.Confidence, promotedRuleConfidence), promotedRuleConfidence)),
			Path:       commonObsPath(ledger, valid),
		}
		inserted, err := store.Insert(ctx, rule, judge)
		if err != nil {
			return added, err
		}
		if inserted {
			added++
		}
		// Either way the evidence is spent: the pattern is now covered by a rule
		// (new or pre-existing per the dedup judge).
		ledger.Remove(valid)
	}
	return added, nil
}

// commonObsPath anchors a promoted rule for path-gone expiry only when every
// member observation points at the same file.
func commonObsPath(ledger *ObsLedger, ids []string) string {
	path := ""
	for i, id := range ids {
		o, ok := ledger.Get(id)
		if !ok {
			continue
		}
		if i == 0 {
			path = o.Path
			continue
		}
		if o.Path != path {
			return ""
		}
	}
	return path
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
