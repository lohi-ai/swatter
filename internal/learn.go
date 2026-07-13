package internal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The post-merge learn flow. Where RunReview learns from its own validator
// (hits/misses inside one review), this flow learns from humans: after PRs
// merge it reads back the feedback on swatter's comments, folds it into the
// rule book's scores, records evidence of gaps, promotes patterns that have
// proven themselves across PRs, and commits the result to the base branch.
//
// Reading feedback is deterministic and free; the LLM steps (clustering,
// dedup) are the cost. So the flow is batch-shaped: score every PR first,
// then promote ONCE over the accumulated evidence — never once per PR — and
// only when this run actually added evidence the previous run hasn't seen.

const (
	rulesPath   = ".swatter/rules.md"
	pendingPath = ".swatter/pending.md"
)

// learnAPI is the slice of GitHubClient the learn flow needs; tests inject a
// fake without an HTTP server.
type learnAPI interface {
	contentsAPI
	ListReviewComments(ctx context.Context, pr int) ([]ReviewCommentData, error)
	ThreadResolution(ctx context.Context, pr int) (map[int64]bool, error)
}

// FeedbackSummary reports what one learn run did, for the CLI summary line.
// In the batch flow the counters aggregate across every PR folded in.
type FeedbackSummary struct {
	SwatterComments int      // swatter inline comments found
	Signals         int      // of those, how many carried a human signal
	Hits, Misses    int      // rule ids scored up / down
	ObsAdded        int      // new observations recorded (post-dedup)
	RulesPromoted   int      // observation clusters promoted into rules
	Committed       []string // files actually committed
}

// BatchSummary is FeedbackSummary plus the batch bookkeeping the nightly run
// reports: how many PRs were examined, skipped as already learned-from, or
// unreadable.
type BatchSummary struct {
	FeedbackSummary
	Scanned       int // PRs whose comments were read this run
	SkippedScored int // PRs skipped up front: already folded in by a prior run
	Failed        int // PRs whose feedback could not be read
}

// RunFeedbackBatch executes the learn flow for every PR merged in the lookback
// window — the scheduled sole-writer path. PRs are grouped by base branch (the
// branch their rule book lives on); each branch reads the book once, folds all
// its PRs' feedback deterministically, runs the one promotion pass, and
// commits once per file. A single PR's read failure is counted and skipped —
// one unreadable PR must not sink the nightly run; a branch-level failure
// fails all of that branch's PRs but the other branches still run.
func RunFeedbackBatch(ctx context.Context, cfg Config, gh learnAPI, prs []MergedPR, progress ProgressFn) (BatchSummary, error) {
	if progress == nil {
		progress = func(string) {}
	}
	var sum BatchSummary
	byBranch := map[string][]int{}
	var order []string
	for _, pr := range prs {
		branch := pr.BaseRef
		if branch == "" {
			branch = "main"
		}
		if _, seen := byBranch[branch]; !seen {
			order = append(order, branch)
		}
		byBranch[branch] = append(byBranch[branch], pr.Number)
	}
	for _, branch := range order {
		out, err := runFeedbackBranch(ctx, cfg, gh, branch, byBranch[branch], progress)
		if err != nil {
			progress(fmt.Sprintf("branch %s: %v — %d PR(s) not processed", branch, err, len(byBranch[branch])))
			sum.Failed += len(byBranch[branch])
			continue
		}
		sum.SwatterComments += out.SwatterComments
		sum.Signals += out.Signals
		sum.Hits += out.Hits
		sum.Misses += out.Misses
		sum.ObsAdded += out.ObsAdded
		sum.RulesPromoted += out.RulesPromoted
		sum.Committed = append(sum.Committed, out.Committed...)
		sum.Scanned += out.Scanned
		sum.SkippedScored += out.SkippedScored
		sum.Failed += out.Failed
	}
	return sum, nil
}

// RunFeedback executes the learn flow for one merged PR — the per-merge
// preview path. branch is the PR's base branch. When cfg.RulesCommit is false
// it computes everything and prints the would-be book to stdout instead of
// writing (suggestion mode).
func RunFeedback(ctx context.Context, cfg Config, gh learnAPI, pr int, branch string, progress ProgressFn) (FeedbackSummary, error) {
	out, err := runFeedbackBranch(ctx, cfg, gh, branch, []int{pr}, progress)
	if err == nil && out.prErr != nil {
		err = out.prErr // sole PR unreadable: that IS the run failing
	}
	return out.FeedbackSummary, err
}

// branchOutcome is one branch's share of the batch summary. prErr keeps the
// first per-PR read error so the single-PR entrypoint can surface it.
type branchOutcome struct {
	FeedbackSummary
	Scanned, SkippedScored, Failed int
	prErr                          error
}

// prFeedback pairs a PR with its classified feedback, kept so the CAS commit
// can re-apply the exact same deltas deterministically onto refetched content.
type prFeedback struct {
	pr int
	fb PRFeedback
}

// runFeedbackBranch folds a set of merged PRs (all sharing one base branch)
// into that branch's rule book:
//
//  1. Read the book + ledger once. A PR the book already marks scored is
//     skipped before its comments are even listed — the 72h window overlaps
//     the daily cadence, so most re-scans would be pure re-billing.
//  2. Per PR, deterministically: classify feedback, fold hit/miss scores into
//     the in-memory store, accumulate observations. No LLM calls.
//  3. Promote once over the accumulated ledger — and only when this run added
//     an observation the ledger didn't have (an unchanged ledger was already
//     clustered by whichever run last changed it; PromoteObservations itself
//     also skips when no subset could clear the evidence gate).
//  4. Commit each file once through the CAS loop, re-applying the recorded
//     per-PR deltas onto whatever content the retry fetched.
func runFeedbackBranch(ctx context.Context, cfg Config, gh learnAPI, branch string, prNums []int, progress ProgressFn) (branchOutcome, error) {
	if progress == nil {
		progress = func(string) {}
	}
	var out branchOutcome

	rulesMD, rulesSHA, _, err := gh.GetContent(ctx, rulesPath, branch)
	if err != nil {
		return out, fmt.Errorf("read %s: %w", rulesPath, err)
	}
	pendingMD, pendingSHA, _, err := gh.GetContent(ctx, pendingPath, branch)
	if err != nil {
		return out, fmt.Errorf("read %s: %w", pendingPath, err)
	}
	store := ParseRuleStore(rulesMD)
	ledger := ParseObsLedger(pendingMD)
	now := time.Now()
	today := now.Format("2006-01-02")

	var folded []prFeedback
	newObs := 0
	for _, pr := range prNums {
		if store.HasScored(pr) {
			out.SkippedScored++
			progress(fmt.Sprintf("PR #%d already scored — skipped", pr))
			continue
		}
		comments, err := gh.ListReviewComments(ctx, pr)
		if err != nil {
			out.Failed++
			if out.prErr == nil {
				out.prErr = fmt.Errorf("list review comments: %w", err)
			}
			progress(fmt.Sprintf("PR #%d: list review comments: %v — skipped", pr, err))
			continue
		}
		resolved, err := gh.ThreadResolution(ctx, pr)
		if err != nil {
			// Best-effort: reactions/replies/outdated still classify without it.
			progress(fmt.Sprintf("PR #%d: thread resolution unavailable (%v) — continuing without it", pr, err))
			resolved = nil
		}
		fb := AnalyzeFeedback(pr, today, cfg.BotLogin, comments, resolved)
		out.Scanned++
		out.SwatterComments += fb.SwatterComments
		if fb.Signals == 0 && len(fb.Observations) == 0 {
			continue // silent PR: nothing to score, nothing to record
		}
		out.Signals += fb.Signals
		out.Hits += len(fb.HitRuleIDs)
		out.Misses += len(fb.MissRuleIDs)
		if store.MarkScored(pr) {
			store.Score(fb.HitRuleIDs, fb.MissRuleIDs, now)
		}
		for _, o := range fb.Observations {
			if ledger.Add(o) {
				newObs++
			}
		}
		folded = append(folded, prFeedback{pr: pr, fb: fb})
		progress(fmt.Sprintf("PR #%d feedback: %d swatter comment(s), %d signal(s), %d hit / %d miss rule id(s), %d observation(s)",
			pr, fb.SwatterComments, fb.Signals, len(fb.HitRuleIDs), len(fb.MissRuleIDs), len(fb.Observations)))
	}
	out.ObsAdded = newObs
	if len(folded) == 0 {
		return out, nil // nothing new on this branch
	}
	ledger.Prune(now)

	// The LLM steps, once for the whole branch. The re-insert judge is only
	// built when promotion actually ran (it is only consulted when a promoted
	// rule must be re-vetted against content a concurrent commit changed).
	var judge SameRuleJudge
	var promotedRules []Rule
	if newObs > 0 {
		deps, err := newRunnerDeps(cfg, NewBudget(cfg))
		if err != nil {
			return out, fmt.Errorf("deps: %w", err)
		}
		promoted, err := deps.PromoteObservations(ctx, ledger, store, cfg.PromoteAfter)
		if err != nil {
			// Promotion is an enhancement on top of scoring — degrade, don't fail.
			// The evidence stays in the ledger and re-promotes next run.
			progress(fmt.Sprintf("promotion skipped: %v", err))
		}
		out.RulesPromoted = promoted
		if promoted > 0 {
			progress(fmt.Sprintf("promoted %d observation cluster(s) into rules", promoted))
		}
		judge = deps.sameRuleJudge()
		promotedRules = promotedSince(rulesMD, store)
	}
	var allObs []Observation
	for _, f := range folded {
		allObs = append(allObs, f.fb.Observations...)
	}
	spentIDs := spentObservations(pendingMD, allObs, ledger)

	if !cfg.RulesCommit {
		fmt.Println("swatter learn (suggestion mode — SWATTER_RULES_COMMIT=0): computed rule book:")
		fmt.Print(store.Render())
		fmt.Println("\ncomputed pending observations:")
		fmt.Print(ledger.Render())
		return out, nil
	}

	// Commit, one CAS loop per file. Rules first: if pending then fails, the
	// evidence stays in the ledger and the next merge re-promotes into a dedup
	// reject — self-healing, never double-counted.
	msg := fmt.Sprintf("chore(swatter): learn from %s [skip ci]", describePRs(folded))
	pathExists := repoPathChecker(cfg.RepoRoot)
	changed, err := commitFileCAS(ctx, gh, rulesPath, branch, msg, func(current string) (string, error) {
		s := ParseRuleStore(current)
		// Idempotent scoring: only fold each PR's feedback in once, even across a
		// re-run or a retry after a partial commit. MarkScored persists in the
		// rendered book, so the guard holds across stateless CI runs.
		for _, f := range folded {
			if s.MarkScored(f.pr) {
				s.Score(f.fb.HitRuleIDs, f.fb.MissRuleIDs, now)
			}
		}
		// Re-insert promoted rules. When the fetched book is byte-identical to the
		// one promotion vetted against, the cheap normalized prefilter suffices
		// (nil judge — no re-billing). If a concurrent merge changed it under us,
		// re-run the semantic judge so a paraphrased duplicate that racing commit
		// added is still caught.
		reinsertJudge := SameRuleJudge(nil)
		if current != rulesMD {
			reinsertJudge = judge
		}
		for _, r := range promotedRules {
			if _, err := s.Insert(ctx, r, reinsertJudge); err != nil {
				return "", err
			}
		}
		s.Expire(now, pathExists, 0)
		return s.Render(), nil
	}, &contentSeed{content: rulesMD, sha: rulesSHA})
	if err != nil {
		return out, fmt.Errorf("commit rules: %w", err)
	}
	if changed {
		out.Committed = append(out.Committed, rulesPath)
	}

	changed, err = commitFileCAS(ctx, gh, pendingPath, branch, msg, func(current string) (string, error) {
		l := ParseObsLedger(current)
		for _, o := range allObs {
			l.Add(o)
		}
		l.Prune(now)
		l.RemoveIdentities(spentIDs)
		return l.Render(), nil
	}, &contentSeed{content: pendingMD, sha: pendingSHA})
	if err != nil {
		return out, fmt.Errorf("commit pending: %w", err)
	}
	if changed {
		out.Committed = append(out.Committed, pendingPath)
	}
	if len(out.Committed) > 0 {
		progress(fmt.Sprintf("committed %v to %s", out.Committed, branch))
	}
	return out, nil
}

// describePRs names the folded PRs for the commit message: one PR keeps the
// classic "PR #N feedback", a batch says how many.
func describePRs(folded []prFeedback) string {
	if len(folded) == 1 {
		return fmt.Sprintf("PR #%d feedback", folded[0].pr)
	}
	return fmt.Sprintf("%d merged PRs' feedback", len(folded))
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
