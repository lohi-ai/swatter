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

func TestDedupCandidates_MergesConsensus(t *testing.T) {
	in := []Candidate{
		// Same line, deliberately different wording (as angle A and angle D
		// describe one defect) — must still collapse to a single finding so the
		// diff gets one inline comment, not two.
		{File: "a.go", Line: 5, Summary: "index i <= len(items) runs one past the end", Angle: "A", Severity: SevMajor},
		{File: "a.go", Line: 5, Summary: "loop condition allows out-of-range access", Angle: "C", Severity: SevCritical},
		{File: "b.go", Line: 1, Summary: "other", Angle: "D", Severity: SevMinor},
	}
	got := DedupCandidates(in)
	if len(got) != 2 {
		t.Fatalf("want 2, got %d: %+v", len(got), got)
	}
	// merged entry keeps highest severity and both angles
	if got[0].Severity != SevCritical {
		t.Fatalf("want merged CRITICAL, got %s", got[0].Severity)
	}
	if got[0].Angle != "A,C" {
		t.Fatalf("want merged angle A,C, got %q", got[0].Angle)
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

func TestParseVerdict(t *testing.T) {
	v := parseVerdict("The trace: ```json\n{\"verdict\":\"confirmed\",\"severity\":\"MAJOR\",\"rationale\":\"traced\"}\n```")
	if v.Verdict != VerdictConfirmed || v.Severity != "MAJOR" {
		t.Fatalf("unexpected verdict: %+v", v)
	}
	if got := parseVerdict("nonsense"); got.Verdict != "" {
		t.Fatalf("want empty verdict, got %q", got.Verdict)
	}
}
