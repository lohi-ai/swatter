package internal

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Severity ranks a finding. The order matters: severityRank relies on it for
// sorting and for the fail_on gate.
type Severity string

const (
	SevCritical Severity = "CRITICAL"
	SevMajor    Severity = "MAJOR"
	SevMinor    Severity = "MINOR"
)

func severityRank(s Severity) int {
	switch s {
	case SevCritical:
		return 3
	case SevMajor:
		return 2
	case SevMinor:
		return 1
	default:
		return 0
	}
}

// ParseSeverity normalizes model output ("critical", "Major", …) to a Severity,
// defaulting unknown values to MINOR so a malformed label never silently
// escalates a finding past the fail_on gate.
func ParseSeverity(s string) Severity {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "CRITICAL":
		return SevCritical
	case "MAJOR":
		return SevMajor
	default:
		return SevMinor
	}
}

// Verdict is a validator's judgement on a candidate.
type Verdict string

const (
	VerdictConfirmed Verdict = "CONFIRMED"
	VerdictPlausible Verdict = "PLAUSIBLE"
	VerdictReject    Verdict = "REJECT"
)

// Candidate is what a finder emits: a concrete inputs/state → user-visible
// consequence, anchored at file:line. It is the wire shape the finder agents
// return as a JSON array (structured output), so the json tags are the model
// contract.
type Candidate struct {
	File            string   `json:"file"`
	Line            int      `json:"line"`
	Summary         string   `json:"summary"`
	FailureScenario string   `json:"failure_scenario"`
	Severity        Severity `json:"severity,omitempty"`
	// Angle records which finder lens produced the candidate (A–H), for the
	// ANGLES summary line and rule scoring. Set by the harness, not the model.
	Angle string `json:"angle,omitempty"`
	// RuleIDs lists rule-book entries the finder cited as firing for this
	// candidate; drives hit/miss scoring in the rule lifecycle.
	RuleIDs []string `json:"rule_ids,omitempty"`
}

// Finding is a validated Candidate: the validator's verdict and traced
// rationale attached. Only CONFIRMED/PLAUSIBLE findings reach the report;
// REJECT drops the candidate.
type Finding struct {
	Candidate
	Verdict   Verdict `json:"verdict"`
	Rationale string  `json:"rationale"`
}

// Key identifies a candidate for dedup: same file + line + normalized summary
// is one root cause. Two finders hitting it is a strong prior, not two findings.
func (c Candidate) Key() string {
	return fmt.Sprintf("%s:%d:%s", c.File, c.Line, strings.ToLower(strings.TrimSpace(c.Summary)))
}

// ParseCandidates decodes a finder's JSON array output. It tolerates the model
// wrapping the array in prose or a ```json fence — a recall-biased finder must
// not lose its whole batch to a stray leading sentence.
func ParseCandidates(raw string) ([]Candidate, error) {
	body := extractJSONArray(raw)
	if body == "" {
		return nil, fmt.Errorf("no JSON array found in finder output")
	}
	var out []Candidate
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil, fmt.Errorf("decode candidates: %w", err)
	}
	// Drop entries with no file — a finder emitting {} is noise, not a finding.
	cleaned := out[:0]
	for _, c := range out {
		if strings.TrimSpace(c.File) == "" {
			continue
		}
		if c.Line < 0 {
			c.Line = 0
		}
		cleaned = append(cleaned, c)
	}
	return cleaned, nil
}

// extractJSONArray returns the outermost [...] span in s, or "" if none. It
// scans for the first '[' and the matching ']' by bracket depth, ignoring
// brackets inside JSON strings so a summary containing "]" cannot truncate the
// array early.
func extractJSONArray(s string) string {
	start := strings.IndexByte(s, '[')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
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
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// DedupCandidates collapses candidates sharing a Key into one, merging angles
// and rule ids so a consensus (≥2 finders) is recorded without duplicating the
// report entry.
func DedupCandidates(in []Candidate) []Candidate {
	byKey := map[string]*Candidate{}
	var order []string
	for _, c := range in {
		k := c.Key()
		if existing, ok := byKey[k]; ok {
			existing.Angle = mergeCSV(existing.Angle, c.Angle)
			existing.RuleIDs = mergeStrings(existing.RuleIDs, c.RuleIDs)
			// Keep the highest severity either finder assigned.
			if severityRank(c.Severity) > severityRank(existing.Severity) {
				existing.Severity = c.Severity
			}
			continue
		}
		cp := c
		byKey[k] = &cp
		order = append(order, k)
	}
	out := make([]Candidate, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

// SortFindings orders by severity (critical first), then CONFIRMED before
// PLAUSIBLE, then file/line for stable output.
func SortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if ri, rj := severityRank(f[i].Severity), severityRank(f[j].Severity); ri != rj {
			return ri > rj
		}
		if f[i].Verdict != f[j].Verdict {
			return f[i].Verdict == VerdictConfirmed
		}
		if f[i].File != f[j].File {
			return f[i].File < f[j].File
		}
		return f[i].Line < f[j].Line
	})
}

func mergeCSV(a, b string) string {
	set := map[string]bool{}
	var order []string
	for _, part := range strings.Split(a+","+b, ",") {
		p := strings.TrimSpace(part)
		if p == "" || set[p] {
			continue
		}
		set[p] = true
		order = append(order, p)
	}
	sort.Strings(order)
	return strings.Join(order, ",")
}

func mergeStrings(a, b []string) []string {
	set := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if s == "" || set[s] {
			continue
		}
		set[s] = true
		out = append(out, s)
	}
	return out
}
