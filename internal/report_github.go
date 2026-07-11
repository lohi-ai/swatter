package internal

import (
	"context"
	"fmt"
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

	// Split findings into in-diff (inline comments) and out-of-diff (summary).
	// Findings are severity-sorted, so the first one to claim a (path, line)
	// is the most severe — a guard against a later sweep re-commenting a line.
	dm := BuildDiffMap(packet.Diff)
	seen := map[string]bool{}
	var inline []reviewComment
	var outOfDiff []Finding
	for _, f := range res.Findings {
		if f.Line > 0 && dm.Commentable(f.File, f.Line) {
			key := fmt.Sprintf("%s:%d", f.File, f.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			inline = append(inline, reviewComment{
				Path: f.File, Line: f.Line, Side: "RIGHT", Body: renderInline(f),
			})
		} else {
			outOfDiff = append(outOfDiff, f)
		}
	}

	if len(inline) > 0 {
		body := fmt.Sprintf("🤚 Swatter posted %d inline comment(s). Full summary below.", len(inline))
		if err := r.gh.CreateReview(ctx, r.pr, r.headSHA, body, inline); err != nil {
			// Non-fatal: fall through to the summary comment, which carries
			// every finding anyway.
			r.Progress(fmt.Sprintf("inline review failed (%v) — findings are in the summary", err))
		}
	}

	// Append out-of-diff findings (on unchanged lines of touched functions) to
	// the comment with permalinks, since they can't be inline comments — these
	// are the only findings the compact comment spells out in full.
	if len(outOfDiff) > 0 {
		comment += "\n\n#### Findings outside the diff (unchanged lines)\n"
		for _, f := range outOfDiff {
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("[%s:%d](%s)", f.File, f.Line, r.gh.Permalink(r.headSHA, f.File, f.Line))
			}
			comment += fmt.Sprintf("- %s %s — %s (%s)\n", f.Severity, strings.ToLower(string(f.Verdict)), f.Summary, loc)
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
