package internal

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// effortWords is the set of valid review levels accepted as the optional
// positional in `swatter review [effort] …`. An arg that is not one of these
// (and not a flag) is the target, so `swatter review main..HEAD` reviews the
// range rather than failing as a bad effort.
var effortWords = map[string]bool{
	"auto": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

func isEffortWord(s string) bool { return effortWords[strings.ToLower(strings.TrimSpace(s))] }

// reviewArgs is the parsed shape of `review [effort] [--comment] [target]`.
type reviewArgs struct {
	effort   string // "" when not given
	target   string // "" when not given
	comment  bool
	format   string
	repoRoot string
	base     string // --base override
	head     string // --head override
	help     bool
}

// parseReviewArgs hand-parses the review grammar because the effort level is a
// positional that may precede flags (`review medium --comment 42`), which
// stdlib flag parsing can't express. Positionals: the first, if it is an effort
// word, is the effort; the remaining positional is the target.
func parseReviewArgs(args []string) (reviewArgs, error) {
	ra := reviewArgs{format: "text"}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "--comment" || a == "-comment":
			ra.comment = true
		case a == "--format" || a == "-format":
			ra.format = next()
		case strings.HasPrefix(a, "--format="):
			ra.format = strings.TrimPrefix(a, "--format=")
		case a == "--repo-root":
			ra.repoRoot = next()
		case strings.HasPrefix(a, "--repo-root="):
			ra.repoRoot = strings.TrimPrefix(a, "--repo-root=")
		case a == "--base":
			ra.base = next()
		case strings.HasPrefix(a, "--base="):
			ra.base = strings.TrimPrefix(a, "--base=")
		case a == "--head":
			ra.head = next()
		case strings.HasPrefix(a, "--head="):
			ra.head = strings.TrimPrefix(a, "--head=")
		case a == "-h" || a == "--help" || a == "help":
			ra.help = true
		case strings.HasPrefix(a, "-"):
			return ra, fmt.Errorf("unknown flag %q", a)
		default:
			positional = append(positional, a)
		}
	}

	switch len(positional) {
	case 0:
	case 1:
		if isEffortWord(positional[0]) {
			ra.effort = strings.ToLower(positional[0])
		} else {
			ra.target = positional[0]
		}
	case 2:
		if !isEffortWord(positional[0]) {
			return ra, fmt.Errorf("first argument %q is not an effort level (want auto|low|medium|high|xhigh|max); with two arguments the form is `review <effort> <target>`", positional[0])
		}
		ra.effort = strings.ToLower(positional[0])
		ra.target = positional[1]
	default:
		return ra, fmt.Errorf("too many arguments; usage: review [effort] [--comment] [target]")
	}
	return ra, nil
}

var prURLRe = regexp.MustCompile(`/pull/(\d+)`)

// parsePRTarget reports whether target names a pull request (a bare number or a
// .../pull/N URL) and the number if so.
func parsePRTarget(target string) (int, bool) {
	s := strings.TrimSpace(target)
	if s == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n, true
	}
	if m := prURLRe.FindStringSubmatch(s); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n, true
		}
	}
	return 0, false
}

// parseRange splits a git range (`base..head` or `base...head`) into its ends.
// Swatter always diffs three-dot (merge-base) in BuildPacket, so both forms
// resolve to the same review; this only extracts the endpoints.
func parseRange(s string) (base, head string, ok bool) {
	if i := strings.Index(s, "..."); i >= 0 {
		return s[:i], s[i+3:], true
	}
	if i := strings.Index(s, ".."); i >= 0 {
		return s[:i], s[i+2:], true
	}
	return "", "", false
}

// CmdReview is the standalone review front door: run the same find-then-verify
// pipeline as CI against a local checkout and print findings, or (--comment)
// post them to a GitHub PR. `run` stays the event-driven CI entry point; this
// is a new front door on the same engine, not a new engine.
func CmdReview(args []string) int {
	ra, err := parseReviewArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter review: %v\n", err)
		reviewUsage()
		return 2
	}
	if ra.help {
		reviewUsage()
		return 0
	}

	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter review: config: %v\n", err)
		return 2
	}
	if ra.repoRoot != "" {
		cfg.RepoRoot = ra.repoRoot
	}
	if ra.effort != "" {
		cfg.Effort = Effort(ra.effort) // parseReviewArgs validated it is a level
	}

	ctx := context.Background()
	headRef := ra.head
	if headRef == "" {
		headRef = "HEAD"
	}
	baseRef := ra.base

	prNumber, isPR := parsePRTarget(ra.target)

	if ra.comment {
		return reviewAndComment(ctx, cfg, prNumber, isPR, headRef)
	}

	// Local, stdout-only review. Resolve the base from the target when --base
	// was not given explicitly.
	if baseRef == "" {
		switch {
		case isPR:
			// A PR number as target without --comment: diff against the PR's base
			// branch so a local run mirrors what CI would review.
			gh, err := githubClientForRoot(ctx, cfg.RepoRoot)
			if err != nil || gh == nil {
				fmt.Fprintln(os.Stderr, "swatter review: a PR-number target needs a GitHub token (SWATTER_GITHUB_TOKEN/GITHUB_TOKEN); pass a git ref/range instead, or set --base")
				return 2
			}
			pr, err := gh.GetPR(ctx, prNumber)
			if err != nil {
				fmt.Fprintf(os.Stderr, "swatter review: fetch PR #%d: %v\n", prNumber, err)
				return 2
			}
			baseRef = "origin/" + pr.BaseRef
		case ra.target == "":
			baseRef = resolveDefaultBase(ctx, cfg.RepoRoot)
		default:
			if b, h, ok := parseRange(ra.target); ok {
				baseRef = b
				if h != "" {
					headRef = h
				}
			} else {
				baseRef = ra.target // a plain ref/branch — diff it against HEAD
			}
		}
	}

	progress := func(note string) { fmt.Fprintf(os.Stderr, "swatter: %s\n", note) }
	res, packet, err := RunReview(ctx, RunOptions{
		Config:   cfg,
		BaseRef:  baseRef,
		HeadRef:  headRef,
		Progress: progress,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter review: %v\n", err)
		return 2
	}
	switch ra.format {
	case "json":
		fmt.Println(RenderJSON(res))
	default:
		fmt.Print(RenderMarkdown(res, cfg, packet))
	}
	return ExitCode(cfg, res)
}

// reviewAndComment runs a review and posts it to a PR. --comment is an explicit
// ask for a PR write, so a non-PR target or a missing token is an error, never
// a silent downgrade to stdout.
func reviewAndComment(ctx context.Context, cfg Config, prNumber int, isPR bool, headRef string) int {
	if !isPR {
		fmt.Fprintln(os.Stderr, "swatter review --comment: needs a PR number or URL target (e.g. `swatter review low --comment 42`)")
		return 2
	}
	gh, err := githubClientForRoot(ctx, cfg.RepoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter review: %v\n", err)
		return 2
	}
	if gh == nil {
		fmt.Fprintln(os.Stderr, "swatter review --comment: no GitHub token (set SWATTER_GITHUB_TOKEN or GITHUB_TOKEN, or `swatter config set github-token …`)")
		return 2
	}

	// Transparency: state what each token does before any write (as CI does).
	for _, line := range gh.PreflightTokens(ctx).Render() {
		fmt.Fprintf(os.Stderr, "swatter: %s\n", line)
	}

	pr, err := gh.GetPR(ctx, prNumber)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter review: fetch PR #%d: %v\n", prNumber, err)
		return 2
	}
	baseRef := "origin/" + pr.BaseRef

	// Anchor inline comments to the local HEAD we actually diffed (check out the
	// PR branch to post — see README), falling back to the PR's head sha.
	headSHA := HeadSHA(ctx, cfg.RepoRoot)
	if headSHA == "" {
		headSHA = pr.HeadSHA
	}

	reporter := NewReporter(gh, cfg, prNumber, headSHA)
	if err := reporter.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "swatter review: start report: %v\n", err)
		return 2
	}
	progress := func(note string) {
		fmt.Fprintf(os.Stderr, "swatter: %s\n", note)
		reporter.Progress(note)
	}

	res, packet, err := RunReview(ctx, RunOptions{
		Config:   cfg,
		BaseRef:  baseRef,
		HeadRef:  headRef,
		Progress: progress,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "swatter review: %v\n", err)
		return 2
	}

	// Show the result locally too, then post to the PR.
	fmt.Print(RenderMarkdown(res, cfg, packet))
	if err := reporter.Finish(ctx, res, packet); err != nil {
		fmt.Fprintf(os.Stderr, "swatter review: report: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "swatter: posted review to PR #%d\n", prNumber)
	return 0
}

// githubClientForRoot builds a standalone GitHub client from the origin remote
// of repoRoot. Returns (nil, nil) when no token is present.
func githubClientForRoot(ctx context.Context, repoRoot string) (*GitHubClient, error) {
	owner, repo, err := originRepo(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	return NewGitHubClientForRepo(owner, repo)
}

func reviewUsage() {
	fmt.Fprint(os.Stderr, `swatter review — run a review locally (same pipeline as CI)

Usage:
  swatter review [effort] [--comment] [<target>]

Effort (optional positional): auto | low | medium | high | xhigh | max
  Overrides SWATTER_EFFORT for this run; default is auto (sized from the diff).

Target (optional):
  <empty>        diff of the current branch against its merge-base with the
                 default branch (origin/<default>...HEAD)
  <ref>          a branch/ref to diff HEAD against (three-dot / merge-base)
  <base>..<head> a git range (also accepts <base>...<head>)
  <pr-number>    a pull request (fetches its base/head via the GitHub API)
  <pr-url>       https://github.com/<owner>/<repo>/pull/<n>

Flags:
  --comment      post findings to the GitHub PR (requires a PR-number/URL
                 target and a GitHub token); without it, findings go to stdout
  --format text|json   stdout format when not commenting (default text)
  --base REF     base ref override
  --head REF     head ref override (default HEAD)
  --repo-root P  checkout to review (default .)

Config comes from SWATTER_* env or 'swatter config'; run 'swatter doctor' first
to verify your provider answers before burning a full review.
`)
}
