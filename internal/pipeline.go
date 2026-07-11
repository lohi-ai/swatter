package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/lohi-ai/agentray/agentcore"
)

// ProgressFn receives phase progress notes so the reporter can update the
// sticky comment. nil is fine (silent).
type ProgressFn func(note string)

// Result is the outcome of a full review.
type Result struct {
	Findings     []Finding
	AngleCounts  map[string]int // angle letter → candidates emitted
	Validated    int
	Rejected     int
	Consensus    int // candidates ≥2 finders agreed on
	SweepRan     bool
	SpentUSD     float64
	SpentTokens  int
	TrivialPass  string // non-empty = trivial early exit reason
	// RejectedRuleIDs are rule ids cited by candidates the validator rejected —
	// misses that decay the rule's confidence in the lifecycle pass.
	RejectedRuleIDs []string
	// Briefing is the optional LLM reviewer briefing (summary + walkthrough +
	// quiz). nil when disabled, budget-exhausted, or the pass produced nothing.
	Briefing *Briefing
}

// FiredRuleIDs are the rule ids cited by surviving findings — hits that raise a
// rule's confidence.
func (r Result) FiredRuleIDs() []string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, f.RuleIDs...)
	}
	return out
}

// Pipeline drives the review-pr phases over one packet on agentcore.
type Pipeline struct {
	cfg      Config
	deps     *runnerDeps
	packet   *Packet
	progress ProgressFn
}

// NewPipeline wires a review. budget is the shared ledger.
func NewPipeline(cfg Config, packet *Packet, budget *Budget, progress ProgressFn) (*Pipeline, error) {
	deps, err := newRunnerDeps(cfg, budget)
	if err != nil {
		return nil, err
	}
	if progress == nil {
		progress = func(string) {}
	}
	return &Pipeline{cfg: cfg, deps: deps, packet: packet, progress: progress}, nil
}

// Run executes finders → validators → sweep and returns the validated findings.
func (p *Pipeline) Run(ctx context.Context) (Result, error) {
	res := Result{AngleCounts: map[string]int{}}

	if trivial, reason := p.packet.IsTrivial(); trivial {
		res.TrivialPass = reason
		return res, nil
	}

	// Phase 2 — finders (parallel, packed by diff size).
	groups := packAngles(p.packet.ChangedLines)
	p.progress(fmt.Sprintf("finders: %d runs over %d angles", len(groups), len(AllAngles)))
	cands := p.runFinders(ctx, groups, &res)

	deduped := DedupCandidates(cands)
	for _, c := range deduped {
		if strings.Contains(c.Angle, ",") {
			res.Consensus++
		}
	}

	// Phase 3 — validate CRITICAL/MAJOR; MINOR passes through as plausible.
	var toValidate []Candidate
	var findings []Finding
	for _, c := range deduped {
		if severityRank(c.Severity) >= severityRank(SevMajor) {
			toValidate = append(toValidate, c)
		} else {
			findings = append(findings, Finding{Candidate: c, Verdict: VerdictPlausible,
				Rationale: "MINOR — evident from the diff; not independently validated."})
		}
	}
	p.progress(fmt.Sprintf("validators: %d candidates", len(toValidate)))
	validated := p.runValidators(ctx, toValidate, &res)
	findings = append(findings, validated...)

	// Sweep — diff >500 lines or any confirmed CRITICAL.
	if p.shouldSweep(p.packet.ChangedLines, findings) && !p.deps.budget.Exhausted() {
		res.SweepRan = true
		p.progress("sweep: hunting for missed defects")
		swept := p.runSweep(ctx, findings, &res)
		findings = append(findings, swept...)
	}

	SortFindings(findings)
	res.Findings = findings

	// Reviewer briefing — an LLM summary + walkthrough + quiz layered on top of
	// the deterministic scope/risk lines. Best-effort and budget-gated: a failure
	// or an exhausted budget just omits it, so the review never depends on it.
	if p.cfg.Briefing && !p.deps.budget.Exhausted() {
		p.progress("briefing: summarizing the change for the reviewer")
		if b, err := p.deps.BriefReview(ctx, p.packet, findings); err == nil {
			res.Briefing = b
		}
	}

	res.SpentUSD, res.SpentTokens = p.deps.budget.Spent()
	return res, nil
}

func (p *Pipeline) runFinders(ctx context.Context, groups [][]string, res *Result) []Candidate {
	type out struct {
		cands []Candidate
		angle string
	}
	ch := make(chan out, len(groups))
	var wg sync.WaitGroup
	for _, group := range groups {
		if p.deps.budget.Exhausted() {
			break
		}
		group := group
		wg.Add(1)
		go func() {
			defer wg.Done()
			cands := p.runOneFinder(ctx, group)
			ch <- out{cands: cands, angle: strings.Join(group, "")}
		}()
	}
	go func() { wg.Wait(); close(ch) }()

	var all []Candidate
	var mu sync.Mutex
	for o := range ch {
		mu.Lock()
		for _, letter := range o.angle {
			res.AngleCounts[string(letter)] += 0 // ensure key exists
		}
		for i := range o.cands {
			// If a packed group produced a candidate, attribute to the group's
			// first angle when the model didn't self-tag.
			if o.cands[i].Angle == "" {
				o.cands[i].Angle = string([]rune(o.angle)[0])
			}
			res.AngleCounts[o.cands[i].Angle]++
		}
		all = append(all, o.cands...)
		mu.Unlock()
	}
	return all
}

// runOneFinder dispatches one finder run for a group of angles (usually one).
// wrapupInstruction is the final, toolless turn we inject when a finder or sweep
// hits its turn/tool ceiling before emitting JSON. agentcore truncates such a
// run cleanly but leaves Final as the last (non-JSON) assistant text, so the
// angle would otherwise be silently lost. We resume its transcript and ask it to
// conclude from what it already read.
const wrapupInstruction = "Stop exploring — you have reached your read budget for this pass. Do NOT ask for more tools. Using only what you have already read, output your final JSON array of candidates now. If nothing qualifies, return []. Return the JSON array only, no prose."

// isTruncated reports whether a run stopped because it hit a hard cap rather than
// finishing — the states in which Final holds no final answer and a wrap-up turn
// can salvage the work already done.
func isTruncated(stopReason string) bool {
	switch stopReason {
	case "max_turns", "max_tool_calls", "budget_exhausted":
		return true
	}
	return false
}

// wrapUpCandidates salvages a candidate-producing run (finder or sweep) that was
// truncated before it emitted JSON. It replays the run's transcript through a
// fresh, single-turn, toolless agent (roleAgent drops the toolset when
// MaxToolCalls == 0) and parses whatever candidates the model concludes with.
// Returns nil when the run finished cleanly, the budget is spent, or nothing
// usable comes back — the caller falls back to the empty angle it already had.
func (p *Pipeline) wrapUpCandidates(ctx context.Context, tag, model, soul, charter string, r agentcore.RunResult) []Candidate {
	if !isTruncated(r.StopReason) || p.deps.budget.Exhausted() {
		return nil
	}
	limits := agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 24_000, MaxContextTokens: 200_000}
	ag, err := p.deps.roleAgent(model, soul, charter, limits)
	if err != nil {
		return nil
	}
	// Copy the transcript before appending so we never alias the run's slice, then
	// add the wrap-up as a final user turn (the transcript ends on tool results, so
	// a following user message is well-formed).
	history := append(append([]agentcore.Message{}, r.Messages...),
		agentcore.Message{Role: agentcore.RoleUser, Content: wrapupInstruction})
	ctx = agentcore.WithTraceID(ctx, tag+":wrapup")
	rr, err := p.deps.runContinue(ctx, ag, history, wrapupInstruction)
	if err != nil {
		return nil
	}
	cands, err := ParseCandidates(rr.Final)
	if err != nil {
		return nil
	}
	return cands
}

// normalizeCandidateSeverities defaults a missing severity to MINOR and canonicalizes
// the rest — shared by the finder and its wrap-up path.
func normalizeCandidateSeverities(cands []Candidate) []Candidate {
	for i := range cands {
		if cands[i].Severity == "" {
			cands[i].Severity = SevMinor
		}
		cands[i].Severity = ParseSeverity(string(cands[i].Severity))
	}
	return cands
}

func (p *Pipeline) runOneFinder(ctx context.Context, angles []string) []Candidate {
	model := p.cfg.ModelStrong
	if isCheapEligible(angles, p.packet.ChangedLines) {
		model = p.cfg.ModelCheap
	}
	var charters strings.Builder
	for _, a := range angles {
		charters.WriteString(AngleCharter(a))
		charters.WriteString("\n\n")
	}
	cap := lensCap(p.packet.ChangedLines)
	limits := agentcore.Limits{MaxTurns: 16, MaxToolCalls: 48, MaxToolResultLen: 24_000, MaxContextTokens: 200_000}

	ag, err := p.deps.roleAgent(model, FinderPreamble(), charters.String(), limits)
	if err != nil {
		return nil
	}
	input := fmt.Sprintf("%s\n\n## Diff\n```diff\n%s\n```\n\nYour angle(s): %s. Report up to %d candidates per angle. Return the JSON array only.",
		p.packet.Brief, p.packet.Diff, strings.Join(angles, ", "), cap)
	tag := "finder:" + strings.Join(angles, "")
	ctx = agentcore.WithTraceID(ctx, tag)
	r, err := p.deps.run(ctx, ag, input)
	if err != nil {
		return nil
	}
	cands, perr := ParseCandidates(r.Final)
	if isTruncated(r.StopReason) && (perr != nil || len(cands) == 0) {
		// The finder ran out of turns/tool calls before reporting; recover the
		// angle with one toolless wrap-up turn instead of dropping it silently.
		cands = p.wrapUpCandidates(ctx, tag, model, FinderPreamble(), charters.String(), r)
	} else if perr != nil {
		return nil
	}
	return normalizeCandidateSeverities(cands)
}

func (p *Pipeline) runValidators(ctx context.Context, cands []Candidate, res *Result) []Finding {
	ch := make(chan Finding, len(cands))
	var wg sync.WaitGroup
	for _, c := range cands {
		if p.deps.budget.Exhausted() {
			// Budget gone: keep unvalidated CRITICAL/MAJOR as plausible rather
			// than dropping them silently.
			ch <- Finding{Candidate: c, Verdict: VerdictPlausible,
				Rationale: "budget exhausted before validation — reported unvalidated."}
			continue
		}
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			if f, ok := p.validateOne(ctx, c); ok {
				ch <- f
			} else {
				ch <- Finding{Candidate: c, Verdict: VerdictReject}
			}
		}()
	}
	go func() { wg.Wait(); close(ch) }()

	var out []Finding
	for f := range ch {
		if f.Verdict == VerdictReject {
			res.Rejected++
			res.RejectedRuleIDs = append(res.RejectedRuleIDs, f.RuleIDs...)
			continue
		}
		res.Validated++
		out = append(out, f)
	}
	return out
}

// validateOne runs an independent validator on a candidate. ok=false means
// REJECT (or an unparseable/failed run, which we treat as a drop).
func (p *Pipeline) validateOne(ctx context.Context, c Candidate) (Finding, bool) {
	limits := agentcore.Limits{MaxTurns: 12, MaxToolCalls: 32, MaxToolResultLen: 24_000, MaxContextTokens: 160_000}
	ag, err := p.deps.roleAgent(p.cfg.ModelStrong, ValidatorPrompt(), "", limits)
	if err != nil {
		return Finding{}, false
	}
	cj, _ := json.Marshal(c)
	input := fmt.Sprintf("%s\n\n## Candidate to validate\n```json\n%s\n```\n\nTrace it in the real code and return the JSON verdict object only.",
		p.packet.Brief, string(cj))
	ctx = agentcore.WithTraceID(ctx, "validator:"+findingLoc(Finding{Candidate: c}))
	r, err := p.deps.run(ctx, ag, input)
	if err != nil {
		return Finding{}, false
	}
	v := parseVerdict(r.Final)
	if v.Verdict == VerdictReject || v.Verdict == "" {
		return Finding{}, false
	}
	f := Finding{Candidate: c, Verdict: v.Verdict, Rationale: v.Rationale}
	if v.Severity != "" {
		f.Severity = ParseSeverity(string(v.Severity))
	}
	return f, true
}

// runSweep runs one extra finder that gets the validated list and hunts only
// for defects not already on it.
func (p *Pipeline) runSweep(ctx context.Context, found []Finding, res *Result) []Finding {
	known, _ := json.Marshal(found)
	charter := "# Sweep\n\nYou get the diff and the list of defects already found. Hunt ONLY for defects **not** already on that list — a fresh pass with different eyes. An empty result is a fine outcome; do not restate known findings."
	limits := agentcore.Limits{MaxTurns: 16, MaxToolCalls: 48, MaxToolResultLen: 24_000, MaxContextTokens: 200_000}
	ag, err := p.deps.roleAgent(p.cfg.ModelStrong, FinderPreamble(), charter, limits)
	if err != nil {
		return nil
	}
	input := fmt.Sprintf("%s\n\n## Diff\n```diff\n%s\n```\n\n## Already found (do not restate)\n```json\n%s\n```\n\nReturn the JSON array of NEW candidates only.",
		p.packet.Brief, p.packet.Diff, string(known))
	ctx = agentcore.WithTraceID(ctx, "sweep")
	r, err := p.deps.run(ctx, ag, input)
	if err != nil {
		return nil
	}
	cands, perr := ParseCandidates(r.Final)
	if isTruncated(r.StopReason) && (perr != nil || len(cands) == 0) {
		cands = p.wrapUpCandidates(ctx, "sweep", p.cfg.ModelStrong, FinderPreamble(), charter, r)
	} else if perr != nil {
		return nil
	}
	// Sweep candidates are reported as plausible (single-pass, no validator).
	var out []Finding
	for _, c := range cands {
		c.Angle = "sweep"
		c.Severity = ParseSeverity(string(c.Severity))
		out = append(out, Finding{Candidate: c, Verdict: VerdictPlausible,
			Rationale: "found by sweep pass; not independently validated."})
	}
	return out
}

func (p *Pipeline) shouldSweep(lines int, found []Finding) bool {
	if lines > 500 {
		return true
	}
	for _, f := range found {
		if f.Severity == SevCritical && f.Verdict == VerdictConfirmed {
			return true
		}
	}
	return false
}

// --- packing ---

// packAngles groups the eight angles into finder runs by diff size, per
// review-pr Phase 2 packing rules. Large diffs run every angle alone; small
// diffs pack them to save cost.
func packAngles(changedLines int) [][]string {
	switch {
	case changedLines > 1500:
		// Every angle rides alone.
		out := make([][]string, 0, len(AllAngles))
		for _, a := range AllAngles {
			out = append(out, []string{a})
		}
		return out
	case changedLines < 100:
		// Tiny diff: pack into three runs — bugs together, security alone,
		// cleanup/conformance together.
		return [][]string{{"A", "B", "C"}, {"D"}, {"E", "F", "G", "H"}}
	default:
		// Normal: bug/security angles alone; H pairs with E, G shares.
		return [][]string{{"A"}, {"B"}, {"C"}, {"D"}, {"E", "H"}, {"F", "G"}}
	}
}

// lensCap is the per-lens candidate cap; larger diffs get a higher cap.
func lensCap(changedLines int) int {
	if changedLines > 1500 {
		return 10
	}
	return 6
}

// isCheapEligible: E–G may run a tier down, but only on a small diff.
func isCheapEligible(angles []string, changedLines int) bool {
	if changedLines >= 1500 {
		return false
	}
	for _, a := range angles {
		if a == "A" || a == "B" || a == "C" || a == "D" || a == "H" {
			return false
		}
	}
	return true
}

// --- verdict parsing ---

type verdictObj struct {
	Verdict   Verdict  `json:"verdict"`
	Severity  Severity `json:"severity"`
	Rationale string   `json:"rationale"`
}

func parseVerdict(raw string) verdictObj {
	body := extractJSONObject(raw)
	var v verdictObj
	if body != "" {
		_ = json.Unmarshal([]byte(body), &v)
	}
	v.Verdict = Verdict(strings.ToUpper(strings.TrimSpace(string(v.Verdict))))
	switch v.Verdict {
	case VerdictConfirmed, VerdictPlausible, VerdictReject:
	default:
		v.Verdict = ""
	}
	return v
}

// extractJSONObject returns the outermost {...} span, ignoring braces in
// strings — mirrors extractJSONArray for the validator's single object.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case ch == '\\':
				esc = true
			case ch == '"':
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
