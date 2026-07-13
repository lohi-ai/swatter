package internal

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeLearnAPI is an in-memory learnAPI: files are keyed "branch:path" and
// every PUT is recorded so tests can assert one commit per file per branch.
type fakeLearnAPI struct {
	files       map[string]string
	comments    map[int][]ReviewCommentData
	commentsErr map[int]error
	resolved    map[int]map[int64]bool
	listCalls   []int
	puts        []string
}

func (f *fakeLearnAPI) GetContent(_ context.Context, path, ref string) (string, string, bool, error) {
	c, ok := f.files[ref+":"+path]
	return c, "sha-" + path, ok, nil
}

func (f *fakeLearnAPI) PutContent(_ context.Context, path, branch, _, content, _ string) error {
	key := branch + ":" + path
	f.files[key] = content
	f.puts = append(f.puts, key)
	return nil
}

func (f *fakeLearnAPI) ListReviewComments(_ context.Context, pr int) ([]ReviewCommentData, error) {
	f.listCalls = append(f.listCalls, pr)
	return f.comments[pr], f.commentsErr[pr]
}

func (f *fakeLearnAPI) ThreadResolution(_ context.Context, pr int) (map[int64]bool, error) {
	return f.resolved[pr], nil
}

// swatterFinding builds a swatter inline comment carrying the finding marker,
// authored by the bot login, with the given 👍 count and a current (not
// outdated) anchor.
func swatterFinding(id int64, ruleIDs []string, summary string, up int) ReviewCommentData {
	f := Finding{}
	f.RuleIDs = ruleIDs
	f.Summary = summary
	c := ReviewCommentData{ID: id, Body: renderFindingMarker(f) + "\n**Finding:** " + summary}
	c.User.Login = "github-actions[bot]"
	c.Reactions.Up = up
	pos := 1
	c.Position = &pos
	return c
}

func learnTestConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		RepoRoot:     t.TempDir(),
		BotLogin:     "github-actions[bot]",
		PromoteAfter: 3,
		RulesCommit:  true,
	}
}

// The nightly batch: PRs are grouped per base branch (missing base defaults to
// main), an already-scored PR is skipped before its comments are listed, a
// per-PR read failure is counted without sinking the run, each branch's files
// are committed at most once, and no LLM call is attempted when the promotion
// evidence gate cannot pass (the fake config has no provider — an attempted
// call would surface as a "promotion skipped" note).
func TestRunFeedbackBatch(t *testing.T) {
	seedRules := (&RuleStore{
		Rules:     []Rule{{ID: "r-1", Rule: "Wrap external API calls in withRetry", Confidence: 0.5}},
		ScoredPRs: []int{40},
	}).Render()
	gh := &fakeLearnAPI{
		files: map[string]string{"main:" + rulesPath: seedRules},
		comments: map[int][]ReviewCommentData{
			50: {swatterFinding(1, []string{"r-1"}, "missing retry", 2)}, // hit for r-1
			42: {},                                                       // silent PR
			48: {swatterFinding(2, nil, "unchecked error return", 1)},    // repeat observation, no rule
		},
		commentsErr: map[int]error{30: fmt.Errorf("boom")},
	}
	prs := []MergedPR{
		{Number: 50, BaseRef: "main"},
		{Number: 42, BaseRef: ""}, // missing base ref → defaulted to main
		{Number: 40, BaseRef: "main"},
		{Number: 30, BaseRef: "main"},
		{Number: 48, BaseRef: "release"},
	}
	var notes []string
	sum, err := RunFeedbackBatch(context.Background(), learnTestConfig(t), gh, prs, func(n string) { notes = append(notes, n) })
	if err != nil {
		t.Fatalf("RunFeedbackBatch: %v", err)
	}
	if sum.Scanned != 3 || sum.SkippedScored != 1 || sum.Failed != 1 {
		t.Fatalf("scanned/skipped/failed = %d/%d/%d, want 3/1/1", sum.Scanned, sum.SkippedScored, sum.Failed)
	}
	if sum.Hits != 1 || sum.Misses != 0 || sum.Signals != 2 || sum.ObsAdded != 1 {
		t.Fatalf("hits/misses/signals/obs = %d/%d/%d/%d, want 1/0/2/1", sum.Hits, sum.Misses, sum.Signals, sum.ObsAdded)
	}
	for _, pr := range gh.listCalls {
		if pr == 40 {
			t.Fatal("already-scored PR #40 must be skipped before listing its comments")
		}
	}
	// One commit per file per branch, never more.
	seen := map[string]int{}
	for _, p := range gh.puts {
		seen[p]++
		if seen[p] > 1 {
			t.Fatalf("file %s committed %d times, want once; puts: %v", p, seen[p], gh.puts)
		}
	}
	// The main book carries PR #50's hit and its scored marker (plus the seeded #40).
	book := ParseRuleStore(gh.files["main:"+rulesPath])
	if book.Rules[0].Hits != 1 || book.Rules[0].Confidence <= 0.5 {
		t.Fatalf("r-1 not scored as hit: %+v", book.Rules[0])
	}
	if !book.HasScored(50) || !book.HasScored(40) {
		t.Fatalf("scored-PR markers lost: %v", book.ScoredPRs)
	}
	// PR #48's observation landed in the release ledger; its book records the
	// PR as scored so tomorrow's overlapping window skips it entirely.
	relLedger := ParseObsLedger(gh.files["release:"+pendingPath])
	if len(relLedger.Obs) != 1 || relLedger.Obs[0].PR != 48 {
		t.Fatalf("release ledger = %+v, want PR #48's observation", relLedger.Obs)
	}
	if !ParseRuleStore(gh.files["release:"+rulesPath]).HasScored(48) {
		t.Fatalf("release book must mark PR #48 scored")
	}
	// Sub-gate evidence (weight 1 < 3): promotion must not even be attempted —
	// with no provider configured, an attempt degrades into this note.
	for _, n := range notes {
		if strings.Contains(n, "promotion skipped") {
			t.Fatalf("promotion was attempted despite failing the evidence gate: %q", n)
		}
	}
	if sum.RulesPromoted != 0 {
		t.Fatalf("RulesPromoted = %d, want 0", sum.RulesPromoted)
	}
}

// A second scan of the same window must be a pure no-op: every PR is either
// marked scored (skipped up front) or silent, so nothing is re-listed for
// scored PRs, nothing changes, and nothing is committed.
func TestRunFeedbackBatch_SecondScanIsFree(t *testing.T) {
	gh := &fakeLearnAPI{
		files: map[string]string{},
		comments: map[int][]ReviewCommentData{
			50: {swatterFinding(1, nil, "unchecked error", 1)},
		},
	}
	prs := []MergedPR{{Number: 50, BaseRef: "main"}}
	cfg := learnTestConfig(t)
	if _, err := RunFeedbackBatch(context.Background(), cfg, gh, prs, nil); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	putsAfterFirst := len(gh.puts)
	sum, err := RunFeedbackBatch(context.Background(), cfg, gh, prs, nil)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if sum.SkippedScored != 1 || sum.Scanned != 0 {
		t.Fatalf("second scan skipped/scanned = %d/%d, want 1/0", sum.SkippedScored, sum.Scanned)
	}
	if len(gh.puts) != putsAfterFirst {
		t.Fatalf("second scan committed: %v", gh.puts[putsAfterFirst:])
	}
	for _, pr := range gh.listCalls[1:] {
		if pr == 50 {
			t.Fatal("second scan must not re-list PR #50's comments")
		}
	}
}

// The single-PR entrypoint surfaces its one PR's read failure as an error.
func TestRunFeedback_SinglePRErrorSurfaces(t *testing.T) {
	gh := &fakeLearnAPI{
		files:       map[string]string{},
		commentsErr: map[int]error{7: fmt.Errorf("boom")},
	}
	if _, err := RunFeedback(context.Background(), learnTestConfig(t), gh, 7, "main", nil); err == nil {
		t.Fatal("want error when the sole PR's comments cannot be read")
	}
}
