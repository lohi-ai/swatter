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
	VerdictRefuted   Verdict = "REFUTED"
)

// ParseVerdict normalizes a verifier's verdict word to a Verdict, accepting the
// reference "REFUTED" and the legacy "REJECT" spelling as the same drop verdict.
// An unrecognized word returns "" so the caller can treat it as no-decision.
func ParseVerdict(s string) Verdict {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "CONFIRMED":
		return VerdictConfirmed
	case "PLAUSIBLE":
		return VerdictPlausible
	case "REFUTED", "REJECT":
		return VerdictRefuted
	default:
		return ""
	}
}

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
	// Angle records which finder produced the candidate (A–E, "cleanup", or
	// "sweep"), for the ANGLES summary line and rule scoring. When a location is
	// verified from several angles the merged value is comma-joined ("A,C"). Set
	// by the harness, not the model.
	Angle string `json:"angle,omitempty"`
	// RuleIDs lists rule-book entries the finder cited as firing for this
	// candidate; drives hit/miss scoring in the rule lifecycle.
	RuleIDs []string `json:"rule_ids,omitempty"`
}

// Finding is a validated Candidate: the validator's verdict and traced
// rationale attached. Only CONFIRMED/PLAUSIBLE findings reach the report;
// REFUTED drops the candidate.
type Finding struct {
	Candidate
	Verdict   Verdict `json:"verdict"`
	Rationale string  `json:"rationale"`
}

// IsCleanup reports whether a candidate came only from the cleanup finder (or
// the cleanup lenses) with no correctness angle attached — the reference ranks
// cleanup below every correctness finding. A location that any correctness angle
// also flagged is treated as correctness, not cleanup.
func (c Candidate) IsCleanup() bool {
	cleanup := false
	for _, a := range strings.Split(c.Angle, ",") {
		switch strings.TrimSpace(a) {
		case "A", "B", "C", "D", "E":
			return false // a correctness angle wins
		case AngleCleanup:
			cleanup = true
		}
	}
	return cleanup
}

// GroupKey buckets candidates for the per-location verify pass: every candidate
// at the same file:line (file-level findings at line 0 share one bucket) goes to
// one verifier that judges each independently.
func (c Candidate) GroupKey() string {
	if c.Line > 0 {
		return fmt.Sprintf("%s:%d", c.File, c.Line)
	}
	return c.File + ":0"
}

// Key identifies a candidate for dedup. A finding is anchored to file:line, and
// two finders hitting the same line are almost always the same root cause
// described differently — folding the summary text into the key (as an earlier
// version did) let angle A and angle D each post a separate inline comment on
// one line. So same file + line collapses to one finding (highest severity
// wins, angles merge into a consensus). Line 0 is file-level (no diff line to
// anchor to), so there we keep the normalized summary to avoid over-merging
// distinct file-level findings.
func (c Candidate) Key() string {
	if c.Line > 0 {
		return fmt.Sprintf("%s:%d", c.File, c.Line)
	}
	return fmt.Sprintf("%s:0:%s", c.File, strings.ToLower(strings.TrimSpace(c.Summary)))
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

// MergeFindings collapses surviving findings that share a report Key (same
// file:line, or same file-level summary) into one, so a location the verifier
// confirmed from two angles yields a single inline comment rather than two. The
// merged entry keeps the strongest verdict and severity and the union of angles,
// rule ids, and rationale.
func MergeFindings(in []Finding) []Finding {
	byKey := map[string]*Finding{}
	var order []string
	for _, f := range in {
		k := f.Key()
		if existing, ok := byKey[k]; ok {
			existing.Angle = mergeCSV(existing.Angle, f.Angle)
			existing.RuleIDs = mergeStrings(existing.RuleIDs, f.RuleIDs)
			if severityRank(f.Severity) > severityRank(existing.Severity) {
				existing.Severity = f.Severity
			}
			// CONFIRMED outranks PLAUSIBLE for the merged verdict.
			if f.Verdict == VerdictConfirmed {
				existing.Verdict = VerdictConfirmed
			}
			existing.Rationale = joinRationale(existing.Rationale, f.Rationale)
			continue
		}
		cp := f
		byKey[k] = &cp
		order = append(order, k)
	}
	out := make([]Finding, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

// SortFindings applies the reference ranking: severity (critical first), then
// correctness before cleanup, then CONFIRMED before PLAUSIBLE, then file/line
// for stable output. Severity leads so a forced cut at the findings cap keeps
// the most severe, and so the check-run's fail_on gate reads the worst first.
func SortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if ri, rj := severityRank(f[i].Severity), severityRank(f[j].Severity); ri != rj {
			return ri > rj
		}
		if ci, cj := f[i].IsCleanup(), f[j].IsCleanup(); ci != cj {
			return !ci // correctness (not cleanup) ranks first
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

// joinRationale concatenates two rationales, dropping empties and duplicates.
func joinRationale(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch {
	case a == "":
		return b
	case b == "", a == b:
		return a
	default:
		return a + " " + b
	}
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
