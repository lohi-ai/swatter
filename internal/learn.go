package internal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The post-merge learn flow. Where RunReview learns from its own validator
// (hits/misses inside one review), this flow learns from humans: after a PR
// merges it reads back the feedback on swatter's comments, folds it into the
// rule book's scores, records evidence of gaps, promotes patterns that have
// proven themselves across PRs, and commits the result to the base branch.

const (
	rulesPath   = ".swatter/rules.md"
	pendingPath = ".swatter/pending.md"
)

// FeedbackSummary reports what one learn run did, for the CLI summary line.
type FeedbackSummary struct {
	SwatterComments int      // swatter inline comments found on the PR
	Signals         int      // of those, how many carried a human signal
	Hits, Misses    int      // rule ids scored up / down
	ObsAdded        int      // new observations recorded
	RulesPromoted   int      // observation clusters promoted into rules
	Committed       []string // files actually committed
}

// RunFeedback executes the learn flow for one merged PR. branch is the PR's
// base branch — where the merge landed and where the rule book is committed.
// When cfg.RulesCommit is false it computes everything and prints the would-be
// book to stdout instead of writing (suggestion mode).
func RunFeedback(ctx context.Context, cfg Config, gh *GitHubClient, pr int, branch string, progress ProgressFn) (FeedbackSummary, error) {
	if progress == nil {
		progress = func(string) {}
	}
	var sum FeedbackSummary

	// 1. Read back the merged PR's comment threads.
	comments, err := gh.ListReviewComments(ctx, pr)
	if err != nil {
		return sum, fmt.Errorf("list review comments: %w", err)
	}
	resolved, err := gh.ThreadResolution(ctx, pr)
	if err != nil {
		// Best-effort: reactions/replies/outdated still classify without it.
		progress(fmt.Sprintf("thread resolution unavailable (%v) — continuing without it", err))
		resolved = nil
	}
	today := time.Now().Format("2006-01-02")
	fb := AnalyzeFeedback(pr, today, cfg.BotLogin, comments, resolved)
	sum.SwatterComments = fb.SwatterComments
	sum.Signals = fb.Signals
	sum.Hits, sum.Misses = len(fb.HitRuleIDs), len(fb.MissRuleIDs)
	sum.ObsAdded = len(fb.Observations)
	progress(fmt.Sprintf("feedback: %d swatter comment(s), %d signal(s), %d hit / %d miss rule id(s), %d observation(s)",
		fb.SwatterComments, fb.Signals, sum.Hits, sum.Misses, sum.ObsAdded))

	if sum.Signals == 0 && sum.ObsAdded == 0 {
		return sum, nil // silent PR: nothing to score, nothing to record
	}

	// 2. Fetch the book + ledger as they stand on the base branch and decide
	// promotions once (the LLM steps). The CAS mutations below re-apply these
	// decisions deterministically, so a retry never re-bills the clustering.
	rulesMD, _, _, err := gh.GetContent(ctx, rulesPath, branch)
	if err != nil {
		return sum, fmt.Errorf("read %s: %w", rulesPath, err)
	}
	pendingMD, _, _, err := gh.GetContent(ctx, pendingPath, branch)
	if err != nil {
		return sum, fmt.Errorf("read %s: %w", pendingPath, err)
	}

	deps, err := newRunnerDeps(cfg, NewBudget(cfg))
	if err != nil {
		return sum, fmt.Errorf("deps: %w", err)
	}

	ledger := ParseObsLedger(pendingMD)
	for _, o := range fb.Observations {
		ledger.Add(o)
	}
	ledger.Prune(time.Now())

	store := ParseRuleStore(rulesMD)
	if !store.HasScored(pr) {
		store.Score(fb.HitRuleIDs, fb.MissRuleIDs, time.Now())
	}
	promoted, err := deps.PromoteObservations(ctx, ledger, store, cfg.PromoteAfter)
	if err != nil {
		// Promotion is an enhancement on top of scoring — degrade, don't fail.
		progress(fmt.Sprintf("promotion skipped: %v", err))
	}
	sum.RulesPromoted = promoted
	spentIDs := spentObservations(pendingMD, fb.Observations, ledger)
	promotedRules := promotedSince(rulesMD, store)
	if promoted > 0 {
		progress(fmt.Sprintf("promoted %d observation cluster(s) into rules", promoted))
	}

	if !cfg.RulesCommit {
		fmt.Println("swatter learn (suggestion mode — SWATTER_RULES_COMMIT=0): computed rule book:")
		fmt.Print(store.Render())
		fmt.Println("\ncomputed pending observations:")
		fmt.Print(ledger.Render())
		return sum, nil
	}

	// 3. Commit, one CAS loop per file. Rules first: if pending then fails, the
	// evidence stays in the ledger and the next merge re-promotes into a dedup
	// reject — self-healing, never double-counted.
	msg := fmt.Sprintf("chore(swatter): learn from PR #%d feedback [skip ci]", pr)
	pathExists := repoPathChecker(cfg.RepoRoot)
	changed, err := commitFileCAS(ctx, gh, rulesPath, branch, msg, func(current string) (string, error) {
		s := ParseRuleStore(current)
		// Idempotent scoring: only fold this PR's feedback in once, even across a
		// re-run or a retry after a partial commit. MarkScored persists in the
		// rendered book, so the guard holds across stateless CI runs.
		if s.MarkScored(pr) {
			s.Score(fb.HitRuleIDs, fb.MissRuleIDs, time.Now())
		}
		for _, r := range promotedRules {
			// Re-insert with the cheap normalized prefilter only: the LLM judge
			// already vetted this rule against the book fetched moments ago.
			if _, err := s.Insert(ctx, r, nil); err != nil {
				return "", err
			}
		}
		s.Expire(time.Now(), pathExists, 0)
		return s.Render(), nil
	})
	if err != nil {
		return sum, fmt.Errorf("commit rules: %w", err)
	}
	if changed {
		sum.Committed = append(sum.Committed, rulesPath)
	}

	changed, err = commitFileCAS(ctx, gh, pendingPath, branch, msg, func(current string) (string, error) {
		l := ParseObsLedger(current)
		for _, o := range fb.Observations {
			l.Add(o)
		}
		l.Prune(time.Now())
		l.RemoveIdentities(spentIDs)
		return l.Render(), nil
	})
	if err != nil {
		return sum, fmt.Errorf("commit pending: %w", err)
	}
	if changed {
		sum.Committed = append(sum.Committed, pendingPath)
	}
	if len(sum.Committed) > 0 {
		progress(fmt.Sprintf("committed %v to %s", sum.Committed, branch))
	}
	return sum, nil
}

// spentObservations returns the identities that promotion consumed: entries
// present in the pre-promotion ledger (original file + this run's additions) but
// gone from the post-promotion one. Prune removals are recomputed inside the CAS
// mutation, so only promotion-spent entries matter here.
//
// It returns (PR, normalized-note) identities, not generated ids: the CAS
// mutation refetches a possibly-changed pending.md and reassigns ids, so an id
// captured here (o-<date>-<seq>) can collide with a concurrent run's unrelated
// entry — removing by identity instead deletes exactly the observation Add would
// have deduped, and nothing else.
func spentObservations(originalMD string, added []Observation, after *ObsLedger) []obsIdentity {
	before := ParseObsLedger(originalMD)
	for _, o := range added {
		before.Add(o)
	}
	still := map[obsIdentity]bool{}
	for _, o := range after.Obs {
		still[o.identity()] = true
	}
	var spent []obsIdentity
	for _, o := range before.Obs {
		if !still[o.identity()] {
			spent = append(spent, o.identity())
		}
	}
	return spent
}

// promotedSince returns rules present in store but not in the original book —
// the rules promotion inserted this run, for deterministic re-insertion inside
// the CAS retry.
func promotedSince(originalMD string, store *RuleStore) []Rule {
	orig := map[string]bool{}
	for _, r := range ParseRuleStore(originalMD).Rules {
		orig[r.ID] = true
	}
	var out []Rule
	for _, r := range store.Rules {
		if !orig[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// repoPathChecker returns a path-existence check rooted at the checkout, or
// nil when root doesn't look like a git checkout of the repo — expiring every
// path-anchored rule because the learn flow ran from the wrong directory would
// be far worse than skipping path-gone expiry for one run.
func repoPathChecker(root string) func(string) bool {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil
	}
	return func(p string) bool {
		_, err := os.Stat(filepath.Join(root, p))
		return err == nil
	}
}
