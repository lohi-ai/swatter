package internal

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RenderJSON returns the findings as a JSON array — the Action step output
// (steps.swatter.outputs.findings) for downstream jobs.
func RenderJSON(res Result) string {
	b, err := json.Marshal(res.Findings)
	if err != nil || len(res.Findings) == 0 {
		return "[]"
	}
	return string(b)
}

// RenderMarkdown renders the summary comment body: the review-pr finding format,
// ordered by severity, plus the ANGLES line. Used verbatim as the sticky
// comment body and printed to stdout by `swatter run`.
func RenderMarkdown(res Result, cfg Config) string {
	var b strings.Builder
	b.WriteString("### 🪰 Swatter review\n\n")

	if res.TrivialPass != "" {
		fmt.Fprintf(&b, "**PASS** — %s.\n", res.TrivialPass)
		return b.String()
	}

	confirmed, plausible := 0, 0
	for _, f := range res.Findings {
		if f.Verdict == VerdictConfirmed {
			confirmed++
		} else {
			plausible++
		}
	}

	if len(res.Findings) == 0 {
		b.WriteString("**PASS** — no findings survived validation.\n\n")
	} else {
		fmt.Fprintf(&b, "**%d finding(s)** — %d confirmed, %d plausible.\n\n", len(res.Findings), confirmed, plausible)
		for i, f := range res.Findings {
			line := f.Line
			loc := f.File
			if line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, line)
			}
			fmt.Fprintf(&b, "**[%d] %s %s** — `%s`\n", i+1, f.Severity, strings.ToLower(string(f.Verdict)), loc)
			fmt.Fprintf(&b, "- Issue: %s\n", f.Summary)
			if f.FailureScenario != "" {
				fmt.Fprintf(&b, "- Scenario: %s\n", f.FailureScenario)
			}
			if f.Rationale != "" {
				fmt.Fprintf(&b, "- Validator: %s\n", f.Rationale)
			}
			b.WriteString("\n")
		}
	}

	fmt.Fprintf(&b, "<sub>ANGLES: %s | validated=%d rejected=%d consensus=%d sweep=%s · $%.2f / %d tok</sub>\n",
		angleLine(res.AngleCounts), res.Validated, res.Rejected, res.Consensus,
		sweepStr(res.SweepRan), res.SpentUSD, res.SpentTokens)
	return b.String()
}

func angleLine(counts map[string]int) string {
	var parts []string
	for _, a := range AllAngles {
		parts = append(parts, fmt.Sprintf("%s=%d", a, counts[a]))
	}
	return strings.Join(parts, " ")
}

func sweepStr(ran bool) string {
	if ran {
		return "ran"
	}
	return "skipped"
}
