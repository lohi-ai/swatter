package internal

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// Reporter posts a review to GitHub: an in-progress check run + sticky
// live-progress comment up front, then inline review comments, a final summary
// comment, and the check-run conclusion. All writes go through the harness's
// token; the review agents never hold it.
type Reporter struct {
	gh       *GitHubClient
	cfg      Config
	pr       int
	headSHA  string
	checkID  int64
	stickyID int64
	tracker  *ProgressTracker
}

// NewReporter builds a reporter for a PR. gh may be nil (no token) → the caller
// falls back to stdout.
func NewReporter(gh *GitHubClient, cfg Config, pr int, headSHA string) *Reporter {
	return &Reporter{gh: gh, cfg: cfg, pr: pr, headSHA: headSHA, tracker: &ProgressTracker{}}
}

// Start opens the check run and the sticky comment (found-and-reused if a prior
// run left one). Safe to call when gh is nil (no-op).
func (r *Reporter) Start(ctx context.Context) error {
	if r.gh == nil {
		return nil
	}
	id, err := r.gh.CreateCheckRun(ctx, r.headSHA)
	if err != nil {
		return fmt.Errorf("create check run: %w", err)
	}
	r.checkID = id
	existing, err := r.gh.FindStickyComment(ctx, r.pr, StickyMarker)
	if err != nil {
		return fmt.Errorf("find sticky: %w", err)
	}
	r.stickyID = existing
	sid, err := r.gh.UpsertStickyComment(ctx, r.pr, r.stickyID, r.tracker.RenderLive())
	if err != nil {
		return fmt.Errorf("open sticky: %w", err)
	}
	r.stickyID = sid
	return nil
}

// Progress is the ProgressFn handed to the pipeline: it records the note and
// refreshes the sticky comment. Failures to update are non-fatal (best effort).
func (r *Reporter) Progress(note string) {
	r.tracker.Note(note)
	if r.gh == nil || r.stickyID == 0 {
		return
	}
	_, _ = r.gh.UpsertStickyComment(context.Background(), r.pr, r.stickyID, r.tracker.RenderLive())
}

// Finish posts the inline comments, the final summary, and completes the check
// run. packet supplies the diff for line-mapping.
func (r *Reporter) Finish(ctx context.Context, res Result, packet *Packet) error {
	// The check-run details page carries the full per-finding summary; the PR
	// comment stays compact (findings are posted inline) to avoid doubling.
	checkSummary := RenderMarkdown(res, r.cfg, packet)
	comment := RenderSummaryComment(res, packet)
	if r.gh == nil {
		return nil
	}

	// Reconcile against Swatter's existing threads on this PR (multi-round
	// re-review): drop findings that already have an open comment thread so they
	// aren't duplicated, and collect stale threads (finding gone) to resolve.
	// The check-run summary and sticky comment still reflect the full review —
	// only inline comment posting is deduped. Best-effort: a fetch failure falls
	// back to the pre-reconcile behavior (post everything, resolve nothing).
	postSet := res.Findings
	var toResolve []string
	if threads, err := r.gh.ReviewThreads(ctx, r.pr); err != nil {
		r.Progress(fmt.Sprintf("thread reconcile skipped (%v) — posting all findings", err))
		// Diagnostics go to stderr because Reporter.Progress only reaches the
		// per-run sticky comment (overwritten by the final render), so a
		// reconcile that silently no-ops leaves no trace in the Action log.
		fmt.Fprintf(os.Stderr, "swatter: reconcile: ReviewThreads failed: %v — posting all %d finding(s)\n", err, len(res.Findings))
	} else {
		postSet, toResolve = reconcile(res.Findings, threads, r.cfg.BotLogin)
		fmt.Fprintf(os.Stderr, "swatter: reconcile: %d thread(s) fetched, botLogin=%q, %d finding(s) → %d to post, %d to resolve\n",
			len(threads), r.cfg.BotLogin, len(res.Findings), len(postSet), len(toResolve))
	}

	// Split the post-set into in-diff (inline comments) and out-of-diff
	// (summary). Findings are severity-sorted, so the first one to claim a
	// (path, line) is the most severe — a guard against a later sweep
	// re-commenting a line.
	dm := BuildDiffMap(packet.Diff)
	seen := map[string]bool{}
	var inline []reviewComment
	var inlineFindings []Finding // parallel to inline: the findings posted inline
	var outOfDiff []Finding
	for _, f := range postSet {
		if f.Line > 0 && dm.Commentable(f.File, f.Line) {
			key := fmt.Sprintf("%s:%d", f.File, f.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			inline = append(inline, reviewComment{
				Path: f.File, Line: f.Line, Side: "RIGHT", Body: renderInline(f),
			})
			inlineFindings = append(inlineFindings, f)
		} else {
			outOfDiff = append(outOfDiff, f)
		}
	}

	inlineFailed := false
	if len(inline) > 0 {
		body := fmt.Sprintf("🤚 Swatter posted %d inline comment(s). Full summary below.", len(inline))
		if err := r.gh.CreateReview(ctx, r.pr, r.headSHA, body, inline); err != nil {
			// Non-fatal, but the inline comments never landed — record it so the
			// findings get spelled out in the sticky comment below instead of
			// silently vanishing from the PR.
			r.Progress(fmt.Sprintf("inline review failed (%v) — findings moved to the summary comment", err))
			inlineFailed = true
		}
	}

	// Resolve stale threads (finding gone this round). Best-effort, per the same
	// contract as the inline post: a failure — e.g. the token can't resolve a
	// thread it authored — is a progress note, never a failed check run.
	// Resolution needs SWATTER_RESOLVE_TOKEN with pull-requests:write AND
	// contents:read+write: the default GITHUB_TOKEN is rejected by
	// resolveReviewThread, and so is a token with only pull-requests:write.
	// Without a capable token, skip the loop rather than fire
	// calls that always fail — dedup already kept the persistent findings from
	// re-posting; the stale threads just stay open.
	if len(toResolve) > 0 && !r.gh.CanResolveThreads() {
		r.Progress(fmt.Sprintf("%d stale thread(s) left open — set resolve_token (pull-requests:write + contents:read+write) to auto-resolve", len(toResolve)))
		fmt.Fprintf(os.Stderr, "swatter: reconcile: %d stale thread(s) not resolved — no SWATTER_RESOLVE_TOKEN (GITHUB_TOKEN cannot resolve threads; the token needs pull-requests:write + contents:read+write)\n", len(toResolve))
		toResolve = nil
	}
	resolved := 0
	for _, id := range toResolve {
		if err := r.gh.ResolveReviewThread(ctx, id); err != nil {
			r.Progress(fmt.Sprintf("resolve stale thread failed (%v)", err))
			fmt.Fprintf(os.Stderr, "swatter: reconcile: resolve thread %s failed: %v\n", id, err)
			continue
		}
		resolved++
	}
	if resolved > 0 {
		r.Progress(fmt.Sprintf("resolved %d stale comment thread(s)", resolved))
	}
	if len(toResolve) > 0 {
		fmt.Fprintf(os.Stderr, "swatter: reconcile: resolved %d/%d stale thread(s)\n", resolved, len(toResolve))
	}

	// When the inline review didn't post, the in-diff findings have nowhere else
	// to appear — spell them out in the comment so nothing is lost. (Accurate
	// heading: these ARE on changed lines; the inline POST just failed.)
	if inlineFailed && len(inlineFindings) > 0 {
		comment += "\n\n#### Findings on changed lines (inline comments failed to post)\n"
		for _, f := range inlineFindings {
			comment += r.findingCommentLine(f)
		}
	}

	// Append out-of-diff findings (on unchanged lines of touched functions) to
	// the comment with permalinks, since they can't be inline comments — these
	// are the only findings the compact comment spells out in full.
	if len(outOfDiff) > 0 {
		comment += "\n\n#### Findings outside the diff (unchanged lines)\n"
		for _, f := range outOfDiff {
			comment += r.findingCommentLine(f)
		}
	}

	if _, err := r.gh.UpsertStickyComment(ctx, r.pr, r.stickyID, RenderFinal(boundComment(comment))); err != nil {
		return fmt.Errorf("finalize sticky: %w", err)
	}

	conclusion, title := r.conclusion(res)
	if err := r.gh.CompleteCheckRun(ctx, r.checkID, conclusion, title, checkSummary); err != nil {
		return fmt.Errorf("complete check run: %w", err)
	}
	return nil
}

// conclusion maps findings to a check-run conclusion under fail_on. Only
// CONFIRMED findings can turn it red, and only when fail_on opts into gating.
// The default (fail_on=never) is advisory: findings are surfaced as comments
// and the check stays green, so the PR shows one passing Swatter status.
func (r *Reporter) conclusion(res Result) (conclusion, title string) {
	worstFail := false
	confirmed, total := 0, len(res.Findings)
	for _, f := range res.Findings {
		if f.Verdict == VerdictConfirmed {
			confirmed++
			if r.cfg.Fails(f.Severity) {
				worstFail = true
			}
		}
	}
	switch {
	case res.TrivialPass != "":
		return "success", "No review needed — " + res.TrivialPass
	case worstFail:
		return "failure", fmt.Sprintf("%d finding(s), %d confirmed", total, confirmed)
	case total > 0 && r.cfg.FailOn == FailOnNever:
		// Advisory mode: findings are informational — green check, details in
		// the inline comments. Gating is opt-in via fail_on.
		return "success", fmt.Sprintf("%d finding(s), %d confirmed — advisory (see comments)", total, confirmed)
	case total > 0:
		return "neutral", fmt.Sprintf("%d finding(s), %d confirmed (below fail threshold)", total, confirmed)
	default:
		return "success", "No findings"
	}
}

// findingCommentLine renders one finding as a bullet for the sticky comment,
// linking file:line to a permalink when the finding anchors to a line. Shared by
// the out-of-diff list and the inline-POST-failure fallback.
func (r *Reporter) findingCommentLine(f Finding) string {
	loc := f.File
	if f.Line > 0 {
		loc = fmt.Sprintf("[%s:%d](%s)", f.File, f.Line, r.gh.Permalink(r.headSHA, f.File, f.Line))
	}
	return fmt.Sprintf("- %s %s — %s (%s)\n", f.Severity, strings.ToLower(string(f.Verdict)), f.Summary, loc)
}

// maxCommentBody caps the sticky comment body. GitHub rejects issue-comment
// bodies over 65536 chars; we stay well under to leave room for the sticky
// marker RenderFinal prepends and the truncation notice. A run with many
// out-of-diff findings is what pushes the comment toward the ceiling.
const maxCommentBody = 63_000

// boundComment caps an assembled comment body at GitHub's size limit, cutting on
// a UTF-8 boundary and appending a notice that points at the check-run summary,
// which carries the full review untruncated.
func boundComment(s string) string {
	if len(s) <= maxCommentBody {
		return s
	}
	cut := maxCommentBody
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n\n_… truncated by Swatter (GitHub comment size limit) — the full review is on the check-run summary._\n"
}

func renderInline(f Finding) string {
	var b strings.Builder
	// Invisible marker first: the post-merge feedback pass identifies swatter's
	// comments by it and maps reactions/replies back to the finding's rules.
	if m := renderFindingMarker(f); m != "" {
		b.WriteString(m + "\n")
	}
	fmt.Fprintf(&b, "**🤚 %s %s** — %s\n\n", f.Severity, strings.ToLower(string(f.Verdict)), f.Summary)
	if f.FailureScenario != "" {
		fmt.Fprintf(&b, "*Scenario:* %s\n\n", f.FailureScenario)
	}
	if f.Rationale != "" {
		fmt.Fprintf(&b, "*Validator:* %s\n", f.Rationale)
	}
	return b.String()
}
