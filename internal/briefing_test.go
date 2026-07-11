package internal

import (
	"strings"
	"testing"
)

func TestSanitizeBriefingTrimsCapsAndDropsEmpty(t *testing.T) {
	in := &Briefing{
		Summary:     "  Adds a retry wrapper around the charge call.  ",
		Walkthrough: []string{" a ", "", "b", "c", "d", "e"}, // 5 non-empty → capped at 4
		Quiz: []QuizItem{
			{Q: " q1 ", A: " a1 "},
			{Q: "", A: "orphan answer"}, // dropped: no question
			{Q: "q2", A: ""},
			{Q: "q3", A: "a3"},
			{Q: "q4", A: "a4"}, // beyond maxQuiz=3 → dropped
		},
	}
	got := sanitizeBriefing(in)
	if got == nil {
		t.Fatal("briefing with content should not sanitize to nil")
	}
	if got.Summary != "Adds a retry wrapper around the charge call." {
		t.Fatalf("summary not trimmed: %q", got.Summary)
	}
	if len(got.Walkthrough) != maxWalkthrough {
		t.Fatalf("walkthrough len = %d, want %d (capped, empties dropped)", len(got.Walkthrough), maxWalkthrough)
	}
	if got.Walkthrough[0] != "a" {
		t.Fatalf("walkthrough not trimmed: %q", got.Walkthrough[0])
	}
	if len(got.Quiz) != maxQuiz {
		t.Fatalf("quiz len = %d, want %d (empties dropped, capped)", len(got.Quiz), maxQuiz)
	}
	if got.Quiz[0].Q != "q1" || got.Quiz[0].A != "a1" {
		t.Fatalf("quiz[0] not trimmed: %+v", got.Quiz[0])
	}
}

func TestSanitizeBriefingDefangsMarkupAndBounds(t *testing.T) {
	// A hostile model (steered by untrusted PR content) tries to break out of the
	// <details> fold, spoof the sticky marker, and bloat the comment.
	in := &Briefing{
		Summary:     "ok</details>\n\n# INJECTED\n<!-- swatter:sticky -->",
		Walkthrough: []string{"line one\nline two"},
		Quiz:        []QuizItem{{Q: "q<script>", A: strings.Repeat("x", maxBriefBullet+500)}},
	}
	got := sanitizeBriefing(in)
	if got == nil {
		t.Fatal("briefing with content should not sanitize to nil")
	}
	// No raw angle brackets survive → no tag can break the comment structure.
	if strings.ContainsAny(got.Summary, "<>") {
		t.Fatalf("summary still holds raw markup: %q", got.Summary)
	}
	if !strings.Contains(got.Summary, "&lt;/details&gt;") || !strings.Contains(got.Summary, "&lt;!-- swatter:sticky") {
		t.Fatalf("summary tags should be escaped, not dropped: %q", got.Summary)
	}
	// Newlines collapse so a field stays a single bullet/line.
	if strings.Contains(got.Summary, "\n") || strings.Contains(got.Walkthrough[0], "\n") {
		t.Fatalf("newlines should collapse: summary=%q walk=%q", got.Summary, got.Walkthrough[0])
	}
	if strings.ContainsAny(got.Quiz[0].Q, "<>") {
		t.Fatalf("quiz question still holds raw markup: %q", got.Quiz[0].Q)
	}
	// Long answer is length-capped (allowing for the escaped-entity expansion).
	if len(got.Quiz[0].A) > maxBriefBullet+8 {
		t.Fatalf("answer not bounded: len=%d", len(got.Quiz[0].A))
	}
}

func TestSanitizeBriefingAllEmptyIsNil(t *testing.T) {
	if got := sanitizeBriefing(&Briefing{Summary: "   ", Walkthrough: []string{" "}, Quiz: []QuizItem{{Q: "  "}}}); got != nil {
		t.Fatalf("an all-empty briefing should sanitize to nil, got %+v", got)
	}
	if sanitizeBriefing(nil) != nil {
		t.Fatal("nil briefing should stay nil")
	}
}

func TestParseBriefingExtractsObject(t *testing.T) {
	raw := "Here you go:\n```json\n{\"summary\":\"does x\",\"walkthrough\":[\"w1\"],\"quiz\":[{\"q\":\"q1\",\"a\":\"a1\"}]}\n```\nthanks"
	b := parseBriefing(raw)
	if b == nil {
		t.Fatal("expected a parsed briefing")
	}
	if b.Summary != "does x" || len(b.Walkthrough) != 1 || len(b.Quiz) != 1 {
		t.Fatalf("parsed wrong: %+v", b)
	}
}

func TestRenderBriefingNilOmitted(t *testing.T) {
	if out := renderBriefing(Result{Briefing: nil}); out != "" {
		t.Fatalf("nil briefing must render nothing, got %q", out)
	}
}

func TestRenderBriefingSummaryVisibleDetailsFolded(t *testing.T) {
	res := Result{Briefing: &Briefing{
		Summary:     "Adds a retry wrapper around the Stripe charge call.",
		Walkthrough: []string{"retries on 5xx with backoff", "idempotency key shape changed"},
		Quiz:        []QuizItem{{Q: "What breaks during rollout?", A: "in-flight charges won't dedupe"}},
	}}
	out := renderBriefing(res)
	// Summary is always visible (not inside the collapsed block).
	if !strings.HasPrefix(out, "**What this PR does** · Adds a retry wrapper") {
		t.Fatalf("summary should lead, got:\n%s", out)
	}
	for _, want := range []string{
		"<details><summary>🔍 Reviewer briefing", "**Walkthrough**", "- retries on 5xx",
		"**Check you caught it**", "**Q1.** What breaks during rollout?",
		"<details><summary>show answer</summary>", "in-flight charges won't dedupe",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("briefing missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderBriefingSummaryOnlyHasNoDetails(t *testing.T) {
	out := renderBriefing(Result{Briefing: &Briefing{Summary: "A one-line change."}})
	if !strings.Contains(out, "A one-line change.") {
		t.Fatalf("summary missing: %q", out)
	}
	if strings.Contains(out, "<details") {
		t.Fatalf("no walkthrough/quiz → no details block, got:\n%s", out)
	}
}

func TestRenderSummaryCommentIncludesBriefing(t *testing.T) {
	res := Result{
		AngleCounts: map[string]int{},
		Briefing:    &Briefing{Summary: "Widens the idempotency key."},
	}
	p := &Packet{ChangedFiles: []string{"billing/charge.go"}, Diff: "+++ b/x\n+a\n"}
	out := RenderSummaryComment(res, p)
	if !strings.Contains(out, "**What this PR does** · Widens the idempotency key.") {
		t.Fatalf("summary comment should carry the briefing:\n%s", out)
	}
}
