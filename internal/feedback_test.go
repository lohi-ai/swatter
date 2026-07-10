package internal

import (
	"strings"
	"testing"
)

func swatterComment(id int64, ruleIDs []string, summary string, outdated bool) ReviewCommentData {
	f := Finding{Candidate: Candidate{File: "a.go", Line: 3, Summary: summary, RuleIDs: ruleIDs,
		Severity: SevMajor}, Verdict: VerdictConfirmed}
	c := ReviewCommentData{ID: id, Body: renderInline(f), Path: "a.go", Line: 3}
	if !outdated {
		pos := 5
		c.Position = &pos
	}
	return c
}

func humanReply(id, parent int64, body string) ReviewCommentData {
	pos := 5
	c := ReviewCommentData{ID: id, InReplyToID: parent, Body: body, Position: &pos}
	c.User.Login = "alice"
	return c
}

func TestFindingMarkerRoundTrip(t *testing.T) {
	f := Finding{Candidate: Candidate{Summary: `breaks when x --> y & "z"`, RuleIDs: []string{"r-1", "r-2"},
		Severity: SevCritical}}
	body := renderInline(f)
	// The marker's JSON must not contain a raw "-->" (json.Marshal escapes '>'
	// as >), or the summary would terminate the HTML comment early and
	// the round-trip below would come back truncated.
	markerLine, _, _ := strings.Cut(body, "\n")
	if strings.Count(markerLine, "-->") != 1 || !strings.HasSuffix(markerLine, "-->") {
		t.Fatalf("marker terminator leaked: %q", markerLine)
	}
	m, ok := parseFindingMarker(body)
	if !ok {
		t.Fatal("marker not found")
	}
	if m.Summary != f.Summary || len(m.RuleIDs) != 2 || m.Severity != SevCritical {
		t.Fatalf("round-trip mismatch: %+v", m)
	}
	if _, ok := parseFindingMarker("just a human comment"); ok {
		t.Fatal("false positive on plain text")
	}
}

func TestClassifyReply(t *testing.T) {
	cases := []struct {
		body string
		want int
	}{
		{"good catch, fixed in abc123", 1},
		{"Thanks! will fix", 1},
		{"you're right", 1},
		{"false positive — the guard is upstream", -1},
		{"this is not a bug, it's by design", -1},
		{"incorrect, the value can't be nil here", -1}, // "incorrect" must not read as "correct"
		{"hmm, can you explain?", 0},
	}
	for _, c := range cases {
		if got := classifyReply(c.body); got != c.want {
			t.Errorf("classifyReply(%q) = %d, want %d", c.body, got, c.want)
		}
	}
}

func TestAnalyzeFeedbackSignals(t *testing.T) {
	up := swatterComment(1, []string{"r-a"}, "nil deref", false)
	up.Reactions.Up = 2

	down := swatterComment(2, []string{"r-b"}, "race condition", false)
	fpReply := humanReply(3, 2, "false positive, the mutex is held by the caller")

	resolvedOnly := swatterComment(4, []string{"r-c"}, "missing rollback", false)
	silent := swatterComment(5, []string{"r-d"}, "unbounded retry", false)

	// Positive feedback on a finding no rule produced → repeat observation.
	noRule := swatterComment(6, nil, "SQL built by string concat", false)
	noRuleReply := humanReply(7, 6, "good catch")

	fb := AnalyzeFeedback(42, "2026-07-11",
		[]ReviewCommentData{up, down, fpReply, resolvedOnly, silent, noRule, noRuleReply},
		map[int64]bool{4: true})

	if fb.SwatterComments != 5 {
		t.Fatalf("SwatterComments = %d, want 5", fb.SwatterComments)
	}
	if want := []string{"r-a", "r-c"}; strings.Join(fb.HitRuleIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("HitRuleIDs = %v, want %v", fb.HitRuleIDs, want)
	}
	if len(fb.MissRuleIDs) != 1 || fb.MissRuleIDs[0] != "r-b" {
		t.Fatalf("MissRuleIDs = %v, want [r-b]", fb.MissRuleIDs)
	}
	if len(fb.Observations) != 1 || fb.Observations[0].Kind != ObsRepeat ||
		fb.Observations[0].Note != "SQL built by string concat" {
		t.Fatalf("Observations = %+v", fb.Observations)
	}
	if fb.Signals != 4 { // up, down, resolvedOnly, noRule — silent carries none
		t.Fatalf("Signals = %d, want 4", fb.Signals)
	}
}

func TestAnalyzeFeedbackOutdatedIsWeakHit(t *testing.T) {
	c := swatterComment(1, []string{"r-x"}, "off by one", true) // line changed before merge
	fb := AnalyzeFeedback(1, "2026-07-11", []ReviewCommentData{c}, nil)
	if len(fb.HitRuleIDs) != 1 || fb.HitRuleIDs[0] != "r-x" {
		t.Fatalf("HitRuleIDs = %v, want [r-x]", fb.HitRuleIDs)
	}

	// …but an explicit rejection beats the implicit signal.
	rej := swatterComment(2, []string{"r-y"}, "leak", true)
	rejReply := humanReply(3, 2, "not an issue, the pool reclaims it")
	fb = AnalyzeFeedback(1, "2026-07-11", []ReviewCommentData{rej, rejReply}, nil)
	if len(fb.HitRuleIDs) != 0 || len(fb.MissRuleIDs) != 1 || fb.MissRuleIDs[0] != "r-y" {
		t.Fatalf("explicit negative should win: hits=%v misses=%v", fb.HitRuleIDs, fb.MissRuleIDs)
	}
}

func TestAnalyzeFeedbackMissedBug(t *testing.T) {
	pos := 5
	other := ReviewCommentData{ID: 10, Path: "db/tx.go", Line: 8,
		Body: "This commit path never releases the advisory lock on error."}
	other.User.Login = "bob"
	other.Position = nil // line was changed before merge → acted on

	nit := ReviewCommentData{ID: 11, Path: "a.go", Body: "nit", Position: &pos}
	nit.User.Login = "bob"

	inactioned := ReviewCommentData{ID: 12, Path: "a.go", Position: &pos,
		Body: "I wonder if we should rename this function for clarity?"}
	inactioned.User.Login = "carol"

	fb := AnalyzeFeedback(7, "2026-07-11", []ReviewCommentData{other, nit, inactioned}, nil)
	if len(fb.Observations) != 1 {
		t.Fatalf("Observations = %+v, want exactly the actioned bug report", fb.Observations)
	}
	o := fb.Observations[0]
	if o.Kind != ObsMissed || o.PR != 7 || o.Path != "db/tx.go" {
		t.Fatalf("observation = %+v", o)
	}
}

func TestEventMergedClose(t *testing.T) {
	e := &GitHubEvent{Action: "closed"}
	e.PullRequest.Merged = true
	if !e.IsMergedClose() {
		t.Fatal("closed+merged should trigger the learn flow")
	}
	e.PullRequest.Merged = false
	if e.IsMergedClose() {
		t.Fatal("closed without merge must not trigger")
	}
	e.Action = "opened"
	e.PullRequest.Merged = true
	if e.IsMergedClose() {
		t.Fatal("only the closed action triggers")
	}
}
