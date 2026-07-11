package internal

import "testing"

func TestParseCandidates_TolerantOfProseAndFence(t *testing.T) {
	raw := "Here are my findings:\n```json\n" +
		`[{"file":"a.go","line":3,"summary":"nil deref","failure_scenario":"x==nil","severity":"CRITICAL"}]` +
		"\n```\nThat's all."
	got, err := ParseCandidates(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].File != "a.go" || got[0].Severity != SevCritical {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestParseCandidates_BracketInStringDoesNotTruncate(t *testing.T) {
	raw := `[{"file":"a.go","line":1,"summary":"array[i] out of bounds ] here","failure_scenario":"i>len"}]`
	got, err := ParseCandidates(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
}

func TestParseCandidates_DropsEmptyFile(t *testing.T) {
	raw := `[{"file":"","line":1,"summary":"x"},{"file":"b.go","line":2,"summary":"y"}]`
	got, _ := ParseCandidates(raw)
	if len(got) != 1 || got[0].File != "b.go" {
		t.Fatalf("want only b.go, got %+v", got)
	}
}

func TestParseCandidates_NoArray(t *testing.T) {
	if _, err := ParseCandidates("no findings, all good"); err == nil {
		t.Fatal("want error for missing array")
	}
}

func TestMergeFindings_CollapsesLocation(t *testing.T) {
	in := []Finding{
		// Same line, verified from two angles — must collapse to a single finding
		// so the diff gets one inline comment, not two.
		{Candidate: Candidate{File: "a.go", Line: 5, Summary: "off by one", Angle: "A", Severity: SevMajor}, Verdict: VerdictPlausible, Rationale: "traced A"},
		{Candidate: Candidate{File: "a.go", Line: 5, Summary: "out of range", Angle: "C", Severity: SevCritical}, Verdict: VerdictConfirmed, Rationale: "traced C"},
		{Candidate: Candidate{File: "b.go", Line: 1, Summary: "other", Angle: "D", Severity: SevMinor}, Verdict: VerdictPlausible},
	}
	got := MergeFindings(in)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d: %+v", len(got), got)
	}
	// merged entry keeps highest severity, escalates to CONFIRMED, unions angles.
	if got[0].Severity != SevCritical {
		t.Fatalf("want merged CRITICAL, got %s", got[0].Severity)
	}
	if got[0].Verdict != VerdictConfirmed {
		t.Fatalf("want merged CONFIRMED, got %s", got[0].Verdict)
	}
	if got[0].Angle != "A,C" {
		t.Fatalf("want merged angle A,C, got %q", got[0].Angle)
	}
}

func TestIsCleanup(t *testing.T) {
	if !(Candidate{Angle: "cleanup"}).IsCleanup() {
		t.Error("pure cleanup should be cleanup")
	}
	if (Candidate{Angle: "A,cleanup"}).IsCleanup() {
		t.Error("a correctness angle at the location wins — not cleanup")
	}
	if (Candidate{Angle: "sweep"}).IsCleanup() {
		t.Error("sweep is not cleanup")
	}
}

func TestParseSeverity(t *testing.T) {
	cases := map[string]Severity{
		"critical": SevCritical, "MAJOR": SevMajor, "minor": SevMinor, "garbage": SevMinor, "": SevMinor,
	}
	for in, want := range cases {
		if got := ParseSeverity(in); got != want {
			t.Errorf("%q: want %s got %s", in, want, got)
		}
	}
}

func TestParseVerdict_Aliases(t *testing.T) {
	cases := map[string]Verdict{
		"confirmed": VerdictConfirmed, "PLAUSIBLE": VerdictPlausible,
		"refuted": VerdictRefuted, "reject": VerdictRefuted, // legacy alias
		"garbage": "", "": "",
	}
	for in, want := range cases {
		if got := ParseVerdict(in); got != want {
			t.Errorf("ParseVerdict(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseVerdicts_ArrayAlignedByIndex(t *testing.T) {
	raw := "Here are the verdicts:\n```json\n" +
		`[{"verdict":"confirmed","severity":"MAJOR","rationale":"traced"},` +
		`{"verdict":"reject","rationale":"guarded at line 3"}]` + "\n```"
	got := parseVerdicts(raw)
	if len(got) != 2 {
		t.Fatalf("want 2 verdicts, got %d", len(got))
	}
	if got[0].Verdict != VerdictConfirmed || got[0].Severity != "MAJOR" {
		t.Errorf("verdict[0] = %+v", got[0])
	}
	if got[1].Verdict != VerdictRefuted {
		t.Errorf("verdict[1] REJECT should normalize to REFUTED, got %q", got[1].Verdict)
	}
	if got := parseVerdicts("no array here"); got != nil {
		t.Errorf("no array → nil, got %+v", got)
	}
}
