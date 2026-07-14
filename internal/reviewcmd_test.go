package internal

import "testing"

func TestParseReviewArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		effort  string
		target  string
		comment bool
		format  string
		wantErr bool
	}{
		{"empty", nil, "", "", false, "text", false},
		{"effort only", []string{"high"}, "high", "", false, "text", false},
		{"range target only", []string{"main..HEAD"}, "", "main..HEAD", false, "text", false},
		{"three-dot range", []string{"main...HEAD"}, "", "main...HEAD", false, "text", false},
		{"pr number target", []string{"42"}, "", "42", false, "text", false},
		{"branch name target", []string{"feature-x"}, "", "feature-x", false, "text", false},
		{"effort then comment then pr", []string{"medium", "--comment", "42"}, "medium", "42", true, "text", false},
		{"flags before positionals", []string{"--comment", "high", "42"}, "high", "42", true, "text", false},
		{"format json", []string{"low", "--format", "json"}, "low", "", false, "json", false},
		{"format equals", []string{"--format=json", "low"}, "low", "", false, "json", false},
		{"effort and target", []string{"high", "main..HEAD"}, "high", "main..HEAD", false, "text", false},
		{"two non-effort positionals errors", []string{"foo", "bar"}, "", "", false, "text", true},
		{"unknown flag errors", []string{"--nope"}, "", "", false, "text", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ra, err := parseReviewArgs(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseReviewArgs(%v) = %+v, want error", c.args, ra)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseReviewArgs(%v) error: %v", c.args, err)
			}
			if ra.effort != c.effort || ra.target != c.target || ra.comment != c.comment || ra.format != c.format {
				t.Fatalf("parseReviewArgs(%v) = effort=%q target=%q comment=%v format=%q; want effort=%q target=%q comment=%v format=%q",
					c.args, ra.effort, ra.target, ra.comment, ra.format, c.effort, c.target, c.comment, c.format)
			}
		})
	}
}

func TestParsePRTarget(t *testing.T) {
	cases := []struct {
		in   string
		num  int
		isPR bool
	}{
		{"42", 42, true},
		{"https://github.com/lohi-ai/swatter/pull/99", 99, true},
		{"main..HEAD", 0, false},
		{"feature-branch", 0, false},
		{"", 0, false},
		{"0", 0, false},
		{"-3", 0, false},
	}
	for _, c := range cases {
		n, ok := parsePRTarget(c.in)
		if n != c.num || ok != c.isPR {
			t.Errorf("parsePRTarget(%q) = %d,%v; want %d,%v", c.in, n, ok, c.num, c.isPR)
		}
	}
}

func TestParseRange(t *testing.T) {
	cases := []struct {
		in         string
		base, head string
		ok         bool
	}{
		{"main..HEAD", "main", "HEAD", true},
		{"main...HEAD", "main", "HEAD", true},
		{"origin/main..HEAD", "origin/main", "HEAD", true},
		{"HEAD", "", "", false},
		{"feature-branch", "", "", false},
	}
	for _, c := range cases {
		b, h, ok := parseRange(c.in)
		if b != c.base || h != c.head || ok != c.ok {
			t.Errorf("parseRange(%q) = %q,%q,%v; want %q,%q,%v", c.in, b, h, ok, c.base, c.head, c.ok)
		}
	}
}
