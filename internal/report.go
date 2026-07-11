package internal

import (
	"encoding/json"
	"fmt"
	"sort"
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

// RenderMarkdown renders the full summary body: the scope + risk read, the
// review-pr finding format ordered by severity, plus the ANGLES line. Backs the
// check-run details page and is printed to stdout by `swatter run`. packet may
// be nil (no scope/risk lines then).
func RenderMarkdown(res Result, cfg Config, packet *Packet) string {
	var b strings.Builder
	b.WriteString("### 🤚 Swatter review\n\n")

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

	if sr := renderScopeRisk(res, packet); sr != "" {
		b.WriteString(sr)
		b.WriteString("\n")
	}
	if br := renderBriefing(res); br != "" {
		b.WriteString(br)
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "<sub>ANGLES: %s | validated=%d rejected=%d consensus=%d sweep=%s · $%.2f / %d tok</sub>\n",
		angleLine(res.AngleCounts), res.Validated, res.Rejected, res.Consensus,
		sweepStr(res.SweepRan), res.SpentUSD, res.SpentTokens)
	return b.String()
}

// RenderSummaryComment is the compact sticky-comment body: the counts, a scope
// + risk read of the change, and the ANGLES footer, with no per-finding blocks.
// Every finding is already posted as an inline comment on the diff, so
// re-printing the full blocks here would double each finding on the PR.
// Out-of-diff findings (which have no diff line to anchor an inline comment to)
// are appended by the reporter. packet may be nil (no scope/risk lines then).
func RenderSummaryComment(res Result, packet *Packet) string {
	var b strings.Builder
	b.WriteString("### 🤚 Swatter review\n\n")

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
		fmt.Fprintf(&b, "**%d finding(s)** — %d confirmed, %d plausible. Details are inline on the diff.\n\n",
			len(res.Findings), confirmed, plausible)
	}

	if sr := renderScopeRisk(res, packet); sr != "" {
		b.WriteString(sr)
		b.WriteString("\n")
	}
	if br := renderBriefing(res); br != "" {
		b.WriteString(br)
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "<sub>ANGLES: %s | validated=%d rejected=%d consensus=%d sweep=%s · $%.2f / %d tok</sub>\n",
		angleLine(res.AngleCounts), res.Validated, res.Rejected, res.Consensus,
		sweepStr(res.SweepRan), res.SpentUSD, res.SpentTokens)
	return b.String()
}

// renderBriefing renders the LLM reviewer briefing: a one-line "what this PR
// does" summary always visible, with the walkthrough and quiz folded into a
// <details> block so the comment stays scannable. Answers are nested in their
// own <details> so a reviewer tries each question before peeking. Returns ""
// when there is no briefing (disabled, budget-exhausted, or nothing usable).
func renderBriefing(res Result) string {
	b := res.Briefing
	if b == nil {
		return ""
	}
	var sb strings.Builder
	if b.Summary != "" {
		fmt.Fprintf(&sb, "**What this PR does** · %s\n", b.Summary)
	}
	if len(b.Walkthrough) == 0 && len(b.Quiz) == 0 {
		return sb.String()
	}

	sb.WriteString("\n<details><summary>🔍 Reviewer briefing — walkthrough & quiz</summary>\n\n")
	if len(b.Walkthrough) > 0 {
		sb.WriteString("**Walkthrough**\n\n")
		for _, w := range b.Walkthrough {
			fmt.Fprintf(&sb, "- %s\n", w)
		}
		sb.WriteString("\n")
	}
	if len(b.Quiz) > 0 {
		sb.WriteString("**Check you caught it** — answer before peeking:\n\n")
		for i, q := range b.Quiz {
			fmt.Fprintf(&sb, "**Q%d.** %s\n", i+1, q.Q)
			if q.A != "" {
				fmt.Fprintf(&sb, "<details><summary>show answer</summary>\n\n%s\n</details>\n\n", q.A)
			}
		}
	}
	sb.WriteString("</details>\n")
	return sb.String()
}

// renderScopeRisk frames a review with two deterministic lines — what the PR
// touches (scope) and how much scrutiny it warrants (risk). Both read only the
// packet and the findings, so they cost no tokens and can never contradict the
// inline comments. Returns "" when no packet is available (local CLI paths).
func renderScopeRisk(res Result, p *Packet) string {
	if p == nil {
		return ""
	}
	var b strings.Builder

	// Scope: file count, +/- lines, sensitive areas, and test coverage of the
	// change. "no tests" is only surfaced when real source changed.
	add, del := p.DiffStat()
	fmt.Fprintf(&b, "**Scope** · %s · +%d −%d", quantify(len(p.ChangedFiles), "file"), add, del)
	if areas := priorityAreas(p.ChangedFiles); len(areas) > 0 {
		fmt.Fprintf(&b, " · touches %s", strings.Join(areas, ", "))
	}
	if t := countTests(p.ChangedFiles); t > 0 {
		fmt.Fprintf(&b, " · %s", quantify(t, "test"))
	} else if hasProductionCode(p.ChangedFiles) {
		b.WriteString(" · no tests")
	}
	b.WriteString("\n")

	r := assessRisk(res, p.ChangedFiles)
	fmt.Fprintf(&b, "**Risk** · %s — %s\n", r.label, r.reason)

	if focus := reviewFocus(res, p); focus != "" {
		b.WriteString(focus)
	}
	return b.String()
}

// reviewFocus is the reviewer's "look here first" line: the confirmed findings'
// locations plus the top actionable ask, so a reviewer knows where to spend
// attention without opening every inline thread. Deterministic, one line, and
// capped — it returns "" when there is nothing a reviewer needs steered to (a
// clean review that touched nothing sensitive).
func reviewFocus(res Result, p *Packet) string {
	var confirmed []Finding
	for _, f := range res.Findings {
		if f.Verdict == VerdictConfirmed {
			confirmed = append(confirmed, f)
		}
	}

	var items []string
	// 1. Confirmed findings are the highest-signal places to look; list up to two.
	for i, f := range confirmed {
		if i == 2 {
			items = append(items, fmt.Sprintf("+%d more confirmed", len(confirmed)-2))
			break
		}
		items = append(items, fmt.Sprintf("`%s` (confirmed %s)", findingLoc(f), strings.ToLower(string(f.Severity))))
	}
	// 2. A source change shipping no tests is a reviewer's classic ask.
	if countTests(p.ChangedFiles) == 0 && hasProductionCode(p.ChangedFiles) {
		items = append(items, "no tests — ask for coverage")
	}
	// 3. Nothing confirmed but sensitive paths moved: steer a human pass.
	if len(confirmed) == 0 {
		if areas := priorityAreas(p.ChangedFiles); len(areas) > 0 {
			items = append(items, fmt.Sprintf("hand-check the %s path", strings.Join(areas, "/")))
		}
	}

	if len(items) == 0 {
		return ""
	}
	return "**Review focus** · " + strings.Join(items, " · ") + "\n"
}

// findingLoc renders a finding's anchor as file:line, or just file when it has
// no diff line (file-level finding).
func findingLoc(f Finding) string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
}

// riskLevel is a one-glance verdict on a PR: a text label and the reason behind
// it, derived from finding severity/verdict crossed with what the PR touches.
type riskLevel struct{ label, reason string }

// assessRisk grades the review. A confirmed CRITICAL is High (block); any other
// confirmed finding is Elevated (named by the sensitive path it lands on, if
// any); unconfirmed findings are Moderate; a clean review is Low — nudged up a
// note when it changed money/auth/migration paths without flagging anything.
// Sensitive-path attribution is computed per verdict, so an Elevated reason
// only names a path a *confirmed* finding actually sits on.
func assessRisk(res Result, files []string) riskLevel {
	var critConf, conf int
	confAreas := map[string]bool{}
	allAreas := map[string]bool{}
	for _, f := range res.Findings {
		if a := fileArea(f.File); a != "" {
			allAreas[a] = true
		}
		if f.Verdict != VerdictConfirmed {
			continue
		}
		conf++
		if f.Severity == SevCritical {
			critConf++
		}
		if a := fileArea(f.File); a != "" {
			confAreas[a] = true
		}
	}
	switch {
	case critConf > 0:
		return riskLevel{"High",
			fmt.Sprintf("%s confirmed critical — needs a fix before merge", quantify(critConf, "finding"))}
	case conf > 0:
		reason := fmt.Sprintf("%s confirmed", quantify(conf, "finding"))
		if a := joinAreas(confAreas); a != "" {
			reason += " on the " + a + " path"
		}
		return riskLevel{"Elevated", reason}
	case len(res.Findings) > 0:
		reason := fmt.Sprintf("%s to weigh, none confirmed", quantify(len(res.Findings), "finding"))
		if a := joinAreas(allAreas); a != "" {
			reason += " (" + a + " path touched)"
		}
		return riskLevel{"Moderate", reason}
	case len(priorityAreas(files)) > 0:
		return riskLevel{"Low", "clean, but it changed sensitive paths worth a human pass"}
	default:
		return riskLevel{"Low", "no findings survived validation"}
	}
}

// joinAreas renders a set of sensitive-area names as a sorted, comma-joined
// string ("auth, webhook"), or "" when empty.
func joinAreas(set map[string]bool) string {
	if len(set) == 0 {
		return ""
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// quantify renders a count with a naive plural: 1 → "1 file", 3 → "3 files".
func quantify(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
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
