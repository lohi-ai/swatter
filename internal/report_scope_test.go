package internal

import (
	"strings"
	"testing"
)

func mkFinding(file string, sev Severity, v Verdict) Finding {
	return Finding{Candidate: Candidate{File: file, Line: 1, Summary: "x", Severity: sev}, Verdict: v}
}

func TestDiffStat(t *testing.T) {
	p := &Packet{Diff: strings.Join([]string{
		"--- a/x.go", "+++ b/x.go", "@@ -1 +1,2 @@", "+added one", "+added two", "-removed one", " context",
	}, "\n")}
	add, del := p.DiffStat()
	if add != 2 || del != 1 {
		t.Fatalf("DiffStat = (+%d −%d), want (+2 −1) — file headers must not count", add, del)
	}
}

func TestPriorityAreas(t *testing.T) {
	got := priorityAreas([]string{"internal/auth/session.go", "db/migrations/003.sql", "README.md", "billing/charge.go"})
	want := "auth, migration, money"
	if strings.Join(got, ", ") != want {
		t.Fatalf("priorityAreas = %v, want [%s] (sorted, distinct buckets)", got, want)
	}
}

func TestAssessRiskLevels(t *testing.T) {
	cases := []struct {
		name    string
		res     Result
		files   []string
		emoji   string
		label   string
		wantSub string // substring the reason must contain
	}{
		{"confirmed critical is high",
			Result{Findings: []Finding{mkFinding("a.go", SevCritical, VerdictConfirmed)}},
			[]string{"a.go"}, "🔴", "High", "before merge"},
		{"confirmed major is elevated",
			Result{Findings: []Finding{mkFinding("a.go", SevMajor, VerdictConfirmed)}},
			[]string{"a.go"}, "🟠", "Elevated", "confirmed"},
		{"confirmed on sensitive path names it",
			Result{Findings: []Finding{mkFinding("auth/token.go", SevMajor, VerdictConfirmed)}},
			[]string{"auth/token.go"}, "🟠", "Elevated", "money/auth/migration"},
		{"only plausible is moderate",
			Result{Findings: []Finding{mkFinding("a.go", SevMinor, VerdictPlausible)}},
			[]string{"a.go"}, "🟡", "Moderate", "none confirmed"},
		{"clean but sensitive nudges low",
			Result{},
			[]string{"auth/login.go"}, "🟢", "Low", "sensitive paths"},
		{"clean is low",
			Result{},
			[]string{"README.md"}, "🟢", "Low", "no findings survived"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := assessRisk(tc.res, tc.files)
			if r.emoji != tc.emoji || r.label != tc.label {
				t.Fatalf("risk = %s %s, want %s %s", r.emoji, r.label, tc.emoji, tc.label)
			}
			if !strings.Contains(r.reason, tc.wantSub) {
				t.Fatalf("reason %q missing %q", r.reason, tc.wantSub)
			}
		})
	}
}

func TestRenderScopeRiskScopeLine(t *testing.T) {
	p := &Packet{
		ChangedFiles: []string{"pkg/auth/login.go", "pkg/auth/login_test.go"},
		Diff:         "+++ b/x\n+a\n+b\n-c\n",
	}
	out := renderScopeRisk(Result{}, p)
	for _, want := range []string{"**Scope**", "2 files", "+2 −1", "touches auth", "1 test", "**Risk**"} {
		if !strings.Contains(out, want) {
			t.Fatalf("scope/risk missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderScopeRiskFlagsMissingTests(t *testing.T) {
	// Real source changed with no test file → the scope line calls it out.
	p := &Packet{ChangedFiles: []string{"pkg/svc/handler.go"}, Diff: "+++ b/x\n+a\n"}
	out := renderScopeRisk(Result{}, p)
	if !strings.Contains(out, "no tests") {
		t.Fatalf("expected a 'no tests' nudge for a code-only change:\n%s", out)
	}
	// A docs-only change must not be nagged about tests.
	docs := &Packet{ChangedFiles: []string{"README.md"}, Diff: "+++ b/x\n+a\n"}
	if strings.Contains(renderScopeRisk(Result{}, docs), "no tests") {
		t.Fatal("docs-only change should not be flagged for missing tests")
	}
}

func TestReviewFocus(t *testing.T) {
	// Confirmed finding + no tests → point at the finding and ask for coverage.
	res := Result{Findings: []Finding{
		{Candidate: Candidate{File: "billing/charge.go", Line: 88, Severity: SevMajor}, Verdict: VerdictConfirmed},
	}}
	p := &Packet{ChangedFiles: []string{"billing/charge.go"}, Diff: "+++ b/x\n+a\n"}
	got := reviewFocus(res, p)
	for _, want := range []string{"**Review focus**", "billing/charge.go:88", "confirmed major", "no tests"} {
		if !strings.Contains(got, want) {
			t.Fatalf("focus %q missing %q", got, want)
		}
	}

	// Clean review on a sensitive path → steer a human pass, no finding pointers.
	clean := reviewFocus(Result{}, &Packet{ChangedFiles: []string{"internal/auth/login.go", "internal/auth/login_test.go"}, Diff: "+a"})
	if !strings.Contains(clean, "hand-check the auth path") {
		t.Fatalf("clean sensitive focus = %q, want a hand-check nudge", clean)
	}

	// Nothing to steer to (clean, tested, nothing sensitive) → no line at all.
	if empty := reviewFocus(Result{}, &Packet{ChangedFiles: []string{"internal/util.go", "internal/util_test.go"}, Diff: "+a"}); empty != "" {
		t.Fatalf("expected no focus line for a clean tested change, got %q", empty)
	}

	// More than two confirmed → cap with a "+N more".
	many := Result{Findings: []Finding{
		mkFinding("a.go", SevMajor, VerdictConfirmed),
		mkFinding("b.go", SevMajor, VerdictConfirmed),
		mkFinding("c.go", SevCritical, VerdictConfirmed),
		mkFinding("d.go", SevMajor, VerdictConfirmed),
	}}
	if f := reviewFocus(many, &Packet{ChangedFiles: []string{"a.go"}, Diff: "+a"}); !strings.Contains(f, "+2 more confirmed") {
		t.Fatalf("focus should cap the list: %q", f)
	}
}

func TestRenderSummaryCommentHasScopeAndRisk(t *testing.T) {
	res := Result{Findings: []Finding{mkFinding("auth/token.go", SevMajor, VerdictConfirmed)}, AngleCounts: map[string]int{}}
	p := &Packet{ChangedFiles: []string{"auth/token.go"}, Diff: "+++ b/x\n+a\n"}
	out := RenderSummaryComment(res, p)
	for _, want := range []string{"1 finding(s)", "**Scope**", "**Risk**", "🟠 Elevated", "ANGLES:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary comment missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderSummaryCommentNilPacketOmitsScope(t *testing.T) {
	res := Result{AngleCounts: map[string]int{}}
	out := RenderSummaryComment(res, nil)
	if strings.Contains(out, "**Scope**") || strings.Contains(out, "**Risk**") {
		t.Fatalf("nil packet must not emit scope/risk lines:\n%s", out)
	}
}

func TestRenderSummaryCommentTrivialSkipsScope(t *testing.T) {
	res := Result{TrivialPass: "only lockfile/generated files changed", AngleCounts: map[string]int{}}
	p := &Packet{ChangedFiles: []string{"go.sum"}, Diff: "+++ b/x\n+a\n"}
	out := RenderSummaryComment(res, p)
	if strings.Contains(out, "**Scope**") {
		t.Fatal("a trivial pass has nothing to scope; scope line should be skipped")
	}
}
