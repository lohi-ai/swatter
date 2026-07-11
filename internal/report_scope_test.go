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

func TestDiffStatCountsPlusPlusContent(t *testing.T) {
	// Content lines whose own text starts with ++/-- render as +++/--- inside the
	// hunk; they are real changes, not headers, and must be counted.
	p := &Packet{Diff: strings.Join([]string{
		"diff --git a/opts.md b/opts.md",
		"--- a/opts.md", "+++ b/opts.md",
		"@@ -1,2 +1,2 @@",
		"+++ option added", // content "++ option added"
		"--- flag removed", // content "-- flag removed"
		" untouched",
	}, "\n")}
	add, del := p.DiffStat()
	if add != 1 || del != 1 {
		t.Fatalf("DiffStat = (+%d −%d), want (+1 −1) — ++/-- content must count", add, del)
	}
}

func TestDiffStatMultiFileHeadersNotCounted(t *testing.T) {
	// Two files: each file's ---/+++ headers sit before its hunk and must not be
	// counted, while both files' hunk content is.
	p := &Packet{Diff: strings.Join([]string{
		"diff --git a/a.go b/a.go", "--- a/a.go", "+++ b/a.go", "@@ -1 +1 @@", "+one",
		"diff --git a/b.go b/b.go", "--- a/b.go", "+++ b/b.go", "@@ -1 +1 @@", "-two",
	}, "\n")}
	add, del := p.DiffStat()
	if add != 1 || del != 1 {
		t.Fatalf("DiffStat = (+%d −%d), want (+1 −1)", add, del)
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
		label   string
		wantSub string // substring the reason must contain
	}{
		{"confirmed critical is high",
			Result{Findings: []Finding{mkFinding("a.go", SevCritical, VerdictConfirmed)}},
			[]string{"a.go"}, "High", "before merge"},
		{"confirmed major is elevated",
			Result{Findings: []Finding{mkFinding("a.go", SevMajor, VerdictConfirmed)}},
			[]string{"a.go"}, "Elevated", "confirmed"},
		{"confirmed on sensitive path names the real area",
			Result{Findings: []Finding{mkFinding("auth/token.go", SevMajor, VerdictConfirmed)}},
			[]string{"auth/token.go"}, "Elevated", "on the auth path"},
		{"webhook finding is named webhook, not money/auth/migration",
			Result{Findings: []Finding{mkFinding("webhook/stripe.go", SevMajor, VerdictConfirmed)}},
			[]string{"webhook/stripe.go"}, "Elevated", "on the webhook path"},
		{"confirmed non-sensitive is not tagged by an unconfirmed sensitive finding",
			Result{Findings: []Finding{
				mkFinding("internal/cache.go", SevMajor, VerdictConfirmed),
				mkFinding("auth/token.go", SevMinor, VerdictPlausible),
			}},
			[]string{"internal/cache.go", "auth/token.go"}, "Elevated", "1 finding confirmed"},
		{"only plausible is moderate",
			Result{Findings: []Finding{mkFinding("a.go", SevMinor, VerdictPlausible)}},
			[]string{"a.go"}, "Moderate", "none confirmed"},
		{"clean but sensitive nudges low",
			Result{},
			[]string{"auth/login.go"}, "Low", "sensitive paths"},
		{"clean is low",
			Result{},
			[]string{"README.md"}, "Low", "no findings survived"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := assessRisk(tc.res, tc.files)
			if r.label != tc.label {
				t.Fatalf("risk label = %q, want %q", r.label, tc.label)
			}
			if !strings.Contains(r.reason, tc.wantSub) {
				t.Fatalf("reason %q missing %q", r.reason, tc.wantSub)
			}
		})
	}
}

func TestAssessRiskConfirmedNonSensitiveOmitsPath(t *testing.T) {
	// A confirmed finding off any sensitive path, alongside an unconfirmed one on
	// a sensitive path, must NOT claim the confirmed finding is on that path.
	r := assessRisk(Result{Findings: []Finding{
		mkFinding("internal/cache.go", SevMajor, VerdictConfirmed),
		mkFinding("auth/token.go", SevMinor, VerdictPlausible),
	}}, []string{"internal/cache.go", "auth/token.go"})
	if strings.Contains(r.reason, "on the") {
		t.Fatalf("Elevated reason must not name a path for a non-sensitive confirmed finding: %q", r.reason)
	}
}

func TestRenderScopeRiskScopeLine(t *testing.T) {
	p := &Packet{
		ChangedFiles: []string{"pkg/auth/login.go", "pkg/auth/login_test.go"},
		Diff:         "--- a/x\n+++ b/x\n@@ -1 +1,2 @@\n+a\n+b\n-c\n",
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
	for _, want := range []string{"1 finding(s)", "**Scope**", "**Risk**", "Elevated", "ANGLES:"} {
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

func TestBoundComment(t *testing.T) {
	// Short comments pass through untouched.
	if got := boundComment("small body"); got != "small body" {
		t.Fatalf("short comment altered: %q", got)
	}
	// An oversized comment (e.g. hundreds of out-of-diff findings) is capped under
	// GitHub's limit and carries a pointer to the full check-run summary.
	big := strings.Repeat("A", maxCommentBody+5_000)
	got := boundComment(big)
	if len(got) > maxCommentBody+200 {
		t.Fatalf("bounded comment still too large: %d", len(got))
	}
	if !strings.Contains(got, "truncated by Swatter") {
		t.Fatalf("truncation notice missing: %q", got[len(got)-120:])
	}
	if RenderFinal(got); len(RenderFinal(got)) > 65_536 {
		t.Fatalf("RenderFinal(bounded) exceeds GitHub's 65536 limit: %d", len(RenderFinal(got)))
	}
}
