// Command swatter is the Cursor-Bugbot-style PR reviewer: it builds a review
// packet from a git checkout, runs the review-pr finder/validator pipeline on
// agentcore, and reports findings. BYOK only — bring an Anthropic key or point
// at any OpenAI-compatible gateway.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/lohi-ai/swatter/internal"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(cmdRun(os.Args[2:]))
	case "learn":
		os.Exit(cmdLearn(os.Args[2:]))
	case "init":
		os.Exit(internal.CmdInit(os.Args[2:]))
	case "review":
		os.Exit(internal.CmdReview(os.Args[2:]))
	case "config":
		os.Exit(internal.CmdConfig(os.Args[2:]))
	case "doctor":
		os.Exit(internal.CmdDoctor(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "swatter: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `swatter — PR-review bugbot on agentcore

Usage:
  swatter run    [flags]  Review a PR / branch and report findings (CI entry;
                          driven by the webhook event payload + SWATTER_* env).
                          On a merged pull_request "closed" event this computes
                          the feedback/learn preview without committing.
  swatter learn  [flags]  Learn from merged-PR feedback: score rules from
                          reactions/replies/resolutions, record gap evidence,
                          promote repeated patterns, commit the rule book.
                          --since 72h scans a window (the scheduled sole writer);
                          --pr N backfills one PR.
  swatter init   [flags]  Scaffold the GitHub workflow + set the API-key secret

Standalone (run a review locally before wiring up CI):
  swatter review [effort] [--comment] [<target>]
                          Review a local checkout with the same pipeline as CI.
                          effort: auto|low|medium|high|xhigh|max (optional).
                          target: empty (current branch vs default), a ref/range,
                          or a PR number/URL. --comment posts to the PR.
  swatter config <cmd>    Manage ~/.config/swatter/config.json (set|get|list|path)
                          so you don't have to export SWATTER_* by hand. Env
                          always overrides the file, so CI is unaffected.
  swatter doctor [flags]  Verify config, git context, GitHub token, and do one
                          cheap model round-trip before burning a full review.

Run flags:
  --github-event PATH  webhook payload (default: $GITHUB_EVENT_PATH)
  --repo-root PATH     checkout to review (default: $SWATTER_REPO_ROOT or .)
  --base REF           base ref to diff against (default: from event or origin/main)
  --head REF           head ref (default: HEAD)
  --format json|text   stdout format (default: text)

Learn flags:
  --github-event PATH  webhook payload (default: $GITHUB_EVENT_PATH)
  --repo-root PATH     checkout root (default: $SWATTER_REPO_ROOT or .)
  --pr N               PR number (default: from event; single-PR mode)
  --branch NAME        base branch the book is committed to (default: from event or main)
  --since DURATION     batch mode: scan every PR merged in this lookback
                       (Go duration, e.g. 72h; default $SWATTER_LEARN_SINCE)

Config is read from SWATTER_* env (SWATTER_API_KEY required). See README.
`)
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	eventPath := fs.String("github-event", os.Getenv("GITHUB_EVENT_PATH"), "webhook payload path")
	repoRoot := fs.String("repo-root", "", "checkout root")
	base := fs.String("base", "", "base ref")
	head := fs.String("head", "", "head ref")
	format := fs.String("format", "text", "json|text")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := internal.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter: config: %v\n", err)
		return 2
	}
	if *repoRoot != "" {
		cfg.RepoRoot = *repoRoot
	}

	var event *internal.GitHubEvent
	if *eventPath != "" {
		if event, err = internal.LoadEvent(*eventPath); err != nil {
			fmt.Fprintf(os.Stderr, "swatter: %v\n", err)
			return 2
		}
		if event.IsFork() {
			fmt.Fprintln(os.Stderr, "swatter: fork PR — GITHUB_TOKEN is read-only, no secrets; exiting neutral.")
			return 0
		}
	}

	ctx := context.Background()

	// On-demand mode: an issue_comment only triggers a review when it asks for
	// one ("@swatter review"). The workflow `if:` already filters on this and on
	// commenter permission, so reaching here without the mention means a
	// mis-wired trigger — exit neutral rather than burn a review. The comment
	// payload omits the PR's base/head refs and title/body, so fetch them.
	if event != nil && event.IsIssueComment() {
		if !event.ReviewMentioned() {
			fmt.Fprintln(os.Stderr, "swatter: comment does not mention @swatter review — nothing to do.")
			return 0
		}
		if err := enrichFromPR(ctx, event); err != nil {
			fmt.Fprintf(os.Stderr, "swatter: resolve PR for comment: %v\n", err)
			return 2
		}
	}

	// A pull_request `closed` event is not a review trigger: when the PR merged
	// it computes the would-be rule-book update (compute-only — never commits)
	// so the CI log carries a preview; a close-without-merge is a no-op. The
	// durable write is the scheduled `swatter learn --since` batch, which is the
	// sole writer of .swatter/{rules,pending}.md so concurrent merges never race.
	if event != nil && event.Action == "closed" {
		if !event.IsMergedClose() {
			fmt.Fprintln(os.Stderr, "swatter: PR closed without merging — nothing to learn.")
			return 0
		}
		cfg.RulesCommit = false // compute-only on merge; the daily batch commits
		return runLearnFlow(ctx, cfg, event.PRNumber(), event.PullRequest.Base.Ref)
	}

	// Set up GitHub reporting when a token + PR are available; otherwise the
	// progress notes and results go to stdout/stderr only.
	reporter, progress := setupReporter(ctx, cfg, event, cfg.RepoRoot)

	res, packet, err := internal.RunReview(ctx, internal.RunOptions{
		Config:   cfg,
		Event:    event,
		BaseRef:  *base,
		HeadRef:  *head,
		Progress: progress,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter: %v\n", err)
		return 2
	}

	// Always emit the requested stdout format (the Action reads json output).
	switch *format {
	case "json":
		fmt.Println(internal.RenderJSON(res))
	default:
		fmt.Print(internal.RenderMarkdown(res, cfg, packet))
	}

	if reporter != nil {
		if err := reporter.Finish(ctx, res, packet); err != nil {
			fmt.Fprintf(os.Stderr, "swatter: report: %v\n", err)
		}
	}
	// Expose findings JSON as an Action step output when running under Actions.
	writeActionOutput(res)

	// When the Swatter check run is live it carries the pass/fail verdict
	// (its conclusion is derived from fail_on), so the wrapping Actions job
	// stays green — "Swatter" is the single authoritative check and the job
	// itself is a passive runner. Fall back to the exit code only outside a
	// check-run context (local CLI / scripting).
	if reporter != nil {
		return 0
	}
	return internal.ExitCode(cfg, res)
}

// cmdLearn runs the feedback/learn flow. Two modes:
//   - single PR: --pr N (or a `closed` event payload) — backfill one PR.
//   - batch: --since DURATION (e.g. 72h) — the scheduled sole-writer job. It
//     scans every PR merged in the window and folds each one's feedback in.
//     The window is meant to overlap the previous run: per-PR scoring is
//     idempotent (RuleStore.HasScored), so a missed nightly run self-heals and
//     late-arriving reactions still get counted, without ever double-scoring.
func cmdLearn(args []string) int {
	fs := flag.NewFlagSet("learn", flag.ContinueOnError)
	eventPath := fs.String("github-event", os.Getenv("GITHUB_EVENT_PATH"), "webhook payload path")
	repoRoot := fs.String("repo-root", "", "checkout root")
	pr := fs.Int("pr", 0, "PR number (default: from event)")
	branch := fs.String("branch", "", "base branch (default: from event or main)")
	since := fs.String("since", os.Getenv("SWATTER_LEARN_SINCE"), "batch mode: scan PRs merged within this lookback (Go duration, e.g. 72h)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := internal.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter: config: %v\n", err)
		return 2
	}
	if *repoRoot != "" {
		cfg.RepoRoot = *repoRoot
	}

	// Batch mode wins when a window is given and no single PR is pinned.
	if *since != "" && *pr == 0 {
		dur, err := time.ParseDuration(*since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "swatter learn: bad --since %q: %v\n", *since, err)
			return 2
		}
		return runLearnBatch(context.Background(), cfg, dur)
	}

	if *eventPath != "" && (*pr == 0 || *branch == "") {
		if event, err := internal.LoadEvent(*eventPath); err == nil {
			if *pr == 0 {
				*pr = event.PRNumber()
			}
			if *branch == "" {
				*branch = event.PullRequest.Base.Ref
			}
		}
	}
	if *pr == 0 {
		fmt.Fprintln(os.Stderr, "swatter learn: no PR number (pass --pr, --since, or --github-event)")
		return 2
	}
	if *branch == "" {
		*branch = "main"
	}
	return runLearnFlow(context.Background(), cfg, *pr, *branch)
}

// runLearnBatch is the scheduled sole-writer flow: enumerate every PR merged in
// the lookback window and fold their feedback into the rule book, batched per
// base branch — deterministic scoring per PR, one promotion pass and one commit
// per file per branch, so the LLM cost stays flat as PR volume grows. A single
// PR's failure is logged and skipped inside the batch — one unreadable PR must
// not sink the whole nightly run.
func runLearnBatch(ctx context.Context, cfg internal.Config, window time.Duration) int {
	gh, err := internal.NewGitHubClientFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter learn: github client: %v\n", err)
		return 2
	}
	if gh == nil {
		fmt.Fprintln(os.Stderr, "swatter learn: no GITHUB_TOKEN — cannot read PR feedback.")
		return 2
	}
	since := time.Now().Add(-window)
	prs, err := gh.ListMergedPRs(ctx, since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter learn: list merged PRs: %v\n", err)
		return 1
	}
	fmt.Printf("swatter learn: %d PR(s) merged since %s\n", len(prs), since.Format("2006-01-02 15:04Z"))
	progress := func(note string) { fmt.Fprintf(os.Stderr, "swatter learn: %s\n", note) }
	sum, err := internal.RunFeedbackBatch(ctx, cfg, gh, prs, progress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter learn: %v\n", err)
		return 1
	}
	fmt.Printf("swatter learn: %d scanned, %d already scored, %d failed — %d signal(s): %d hit / %d miss, +%d obs, %d promoted, committed %v\n",
		sum.Scanned, sum.SkippedScored, sum.Failed, sum.Signals, sum.Hits, sum.Misses, sum.ObsAdded, sum.RulesPromoted, sum.Committed)
	if sum.Failed > 0 {
		fmt.Fprintf(os.Stderr, "swatter learn: %d PR(s) failed\n", sum.Failed)
		return 1
	}
	return 0
}

// runLearnFlow is the shared learn entrypoint for `run` (closed event) and
// `learn`. It needs a GitHub token — reading feedback and committing the book
// are both API operations.
func runLearnFlow(ctx context.Context, cfg internal.Config, pr int, branch string) int {
	gh, err := internal.NewGitHubClientFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter learn: github client: %v\n", err)
		return 2
	}
	if gh == nil {
		fmt.Fprintln(os.Stderr, "swatter learn: no GITHUB_TOKEN — cannot read PR feedback.")
		return 2
	}
	progress := func(note string) { fmt.Fprintf(os.Stderr, "swatter learn: %s\n", note) }
	sum, err := internal.RunFeedback(ctx, cfg, gh, pr, branch, progress)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter learn: %v\n", err)
		return 1
	}
	fmt.Printf("swatter learn: PR #%d — %d comment(s), %d signal(s): %d hit / %d miss, +%d observation(s), %d rule(s) promoted, committed %v\n",
		pr, sum.SwatterComments, sum.Signals, sum.Hits, sum.Misses, sum.ObsAdded, sum.RulesPromoted, sum.Committed)
	return 0
}

// setupReporter builds a GitHub reporter when a token + PR number are present.
// Returns the reporter (nil if unavailable) and the ProgressFn to hand the
// pipeline (sticky-comment updater, or a stderr logger otherwise).
// enrichFromPR fills the base/head refs and title/body on an issue_comment
// event by fetching the pull request — the comment payload carries only the PR
// number. Downstream (RunReview base derivation, reporter head anchor) then
// works exactly as it does for a pull_request event.
func enrichFromPR(ctx context.Context, event *internal.GitHubEvent) error {
	gh, err := internal.NewGitHubClientFromEnv()
	if err != nil {
		return err
	}
	if gh == nil {
		return fmt.Errorf("no GITHUB_TOKEN available")
	}
	pr, err := gh.GetPR(ctx, event.PRNumber())
	if err != nil {
		return err
	}
	event.PullRequest.Number = event.PRNumber()
	event.PullRequest.Base.Ref = pr.BaseRef
	event.PullRequest.Head.SHA = pr.HeadSHA
	event.PullRequest.Title = pr.Title
	event.PullRequest.Body = pr.Body
	return nil
}

func setupReporter(ctx context.Context, cfg internal.Config, event *internal.GitHubEvent, repoRoot string) (*internal.Reporter, internal.ProgressFn) {
	stderrProgress := func(note string) { fmt.Fprintf(os.Stderr, "swatter: %s\n", note) }

	gh, err := internal.NewGitHubClientFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter: github client: %v\n", err)
		return nil, stderrProgress
	}
	if gh == nil || event == nil || event.PRNumber() == 0 {
		return nil, stderrProgress
	}

	// Token transparency: before any GitHub write, state which token does what
	// and verify each works, so a maintainer sees exactly how Swatter uses their
	// credentials (and a bad/missing PAT is a clear warning, not an opaque
	// mid-run permission error). Log-only, best-effort.
	for _, line := range gh.PreflightTokens(ctx).Render() {
		stderrProgress(line)
	}

	headSHA := event.PullRequest.Head.SHA
	if headSHA == "" {
		headSHA = internal.HeadSHA(ctx, repoRoot)
	}
	reporter := internal.NewReporter(gh, cfg, event.PRNumber(), headSHA)
	if err := reporter.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "swatter: start report: %v\n", err)
		return nil, stderrProgress
	}
	return reporter, func(note string) {
		stderrProgress(note)
		reporter.Progress(note)
	}
}

// writeActionOutput appends findings JSON to $GITHUB_OUTPUT so downstream steps
// can read steps.swatter.outputs.findings.
func writeActionOutput(res internal.Result) {
	path := os.Getenv("GITHUB_OUTPUT")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	// Multi-line-safe heredoc form required by Actions for JSON values.
	fmt.Fprintf(f, "findings<<SWATTER_EOF\n%s\nSWATTER_EOF\n", internal.RenderJSON(res))
	fmt.Fprintf(f, "finding_count=%d\n", len(res.Findings))
}
