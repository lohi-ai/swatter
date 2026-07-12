package internal

import "testing"

// TestResolveEffort pins the auto-detect thresholds (the "leaner"/cheaper
// tier): the level is the higher of the two dimensions, so either enough files
// OR enough lines lifts it. Boundaries are inclusive on the lower level.
func TestResolveEffort(t *testing.T) {
	cases := []struct {
		name         string
		files, lines int
		want         Effort
	}{
		{"empty", 0, 0, EffortLow},
		{"tiny", 1, 10, EffortLow},
		{"low boundary", 3, 50, EffortLow},
		{"one line over → medium", 3, 51, EffortMedium},
		{"one file over → medium", 4, 50, EffortMedium},
		{"medium boundary", 10, 300, EffortMedium},
		{"lines lift to high", 5, 301, EffortHigh},
		{"files lift to high", 11, 100, EffortHigh},
		{"high boundary", 25, 1000, EffortHigh},
		{"lines lift to xhigh", 10, 1001, EffortXHigh},
		{"files lift to xhigh", 26, 200, EffortXHigh},
		{"sprawling", 200, 20000, EffortXHigh},
		// The higher dimension wins: few files but a huge single-file diff, or
		// many files with few lines each, both escalate.
		{"one giant file", 1, 5000, EffortXHigh},
		{"many trivial files", 40, 40, EffortXHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveEffort(tc.files, tc.lines); got != tc.want {
				t.Errorf("resolveEffort(%d, %d) = %q, want %q", tc.files, tc.lines, got, tc.want)
			}
		})
	}
}

// TestAutoEffortValidates confirms "auto" passes config validation (it is a
// legal SWATTER_EFFORT even though it never reaches EffortProfile).
func TestAutoEffortValidates(t *testing.T) {
	c := Config{
		Provider: ProviderAnthropic,
		APIKey:   "k",
		FailOn:   FailOnNever,
		Effort:   EffortAuto,
	}
	if err := c.validate(); err != nil {
		t.Fatalf("auto effort should validate, got %v", err)
	}
}
