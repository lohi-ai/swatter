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
	case "init":
		os.Exit(internal.CmdInit(os.Args[2:]))
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
  swatter run  [flags]   Review a PR / branch and report findings
  swatter init [flags]   Scaffold the GitHub workflow + set the API-key secret

Run flags:
  --github-event PATH  webhook payload (default: $GITHUB_EVENT_PATH)
  --repo-root PATH     checkout to review (default: $SWATTER_REPO_ROOT or .)
  --base REF           base ref to diff against (default: from event or origin/main)
  --head REF           head ref (default: HEAD)
  --format json|text   stdout format (default: text)

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
		fmt.Print(internal.RenderMarkdown(res, cfg))
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

// setupReporter builds a GitHub reporter when a token + PR number are present.
// Returns the reporter (nil if unavailable) and the ProgressFn to hand the
// pipeline (sticky-comment updater, or a stderr logger otherwise).
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
