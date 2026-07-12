package internal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RunOptions parameterize a single review invocation.
type RunOptions struct {
	Config    Config
	Event     *GitHubEvent // optional; nil for a local run
	BaseRef   string       // override; else derived from event or "origin/main"
	HeadRef   string       // override; else "HEAD"
	Progress  ProgressFn
}

// RunReview builds the packet and runs the pipeline. It is transport-agnostic:
// the caller decides how to report (stdout, GitHub). Reporting lives in the
// caller so the agent never holds a token.
func RunReview(ctx context.Context, opt RunOptions) (Result, *Packet, error) {
	base := opt.BaseRef
	head := opt.HeadRef
	var title, body string
	if opt.Event != nil {
		if base == "" && opt.Event.PullRequest.Base.Ref != "" {
			base = "origin/" + opt.Event.PullRequest.Base.Ref
		}
		title = opt.Event.PullRequest.Title
		body = opt.Event.PullRequest.Body
	}

	ruleBook := loadRuleBook(opt.Config.RepoRoot)

	packet, err := BuildPacket(ctx, PacketInput{
		RepoRoot: opt.Config.RepoRoot,
		BaseRef:  base,
		HeadRef:  head,
		PRTitle:  title,
		PRBody:   body,
		RuleBook: ruleBook,
	})
	if err != nil {
		return Result{}, nil, fmt.Errorf("build packet: %w", err)
	}

	// Auto effort: size the review level from the diff now that the packet is
	// built, before the pipeline (and its EffortProfile) reads Config.Effort. An
	// explicit SWATTER_EFFORT is any other value and passes through untouched.
	if opt.Config.Effort == EffortAuto {
		lvl := resolveEffort(len(packet.ChangedFiles), packet.ChangedLines)
		opt.Config.Effort = lvl
		opt.Progress(fmt.Sprintf("effort auto → %s (%d file(s), %d changed line(s))",
			lvl, len(packet.ChangedFiles), packet.ChangedLines))
	}

	budget := NewBudget(opt.Config)
	pipeline, err := NewPipeline(opt.Config, packet, budget, opt.Progress)
	if err != nil {
		return Result{}, packet, fmt.Errorf("pipeline: %w", err)
	}
	res, err := pipeline.Run(ctx)
	if err != nil {
		return Result{}, packet, fmt.Errorf("run pipeline: %w", err)
	}

	// Rule lifecycle: score fired/rejected rules, learn from confirmed findings,
	// expire stale ones, and persist. Best-effort — a lifecycle error must never
	// fail the review itself.
	if err := ApplyLifecycle(ctx, opt.Config, pipeline.deps, packet, res); err != nil {
		opt.Progress(fmt.Sprintf("rule lifecycle skipped: %v", err))
	}
	return res, packet, nil
}

// ApplyLifecycle runs the rule-book lifecycle after a review: Score → Learn →
// Dedup → Compact/Expire, then persists the updated book. Writing to
// .swatter/rules.md is opt-in (SWATTER_RULES_WRITE=1) — the default is
// suggestion mode (compute the book so a future step can post it as a PR
// suggestion), because committing from CI races concurrent PRs on one file.
func ApplyLifecycle(ctx context.Context, cfg Config, deps *runnerDeps, packet *Packet, res Result) error {
	store := ParseRuleStore(loadRuleBook(cfg.RepoRoot))
	now := time.Now()

	store.Score(res.FiredRuleIDs(), res.RejectedRuleIDs, now)

	var confirmed []Finding
	for _, f := range res.Findings {
		if f.Verdict == VerdictConfirmed {
			confirmed = append(confirmed, f)
		}
	}
	if _, err := deps.LearnRules(ctx, packet, confirmed, store); err != nil {
		return fmt.Errorf("learn: %w", err)
	}

	pathExists := func(p string) bool {
		_, err := os.Stat(filepath.Join(cfg.RepoRoot, p))
		return err == nil
	}
	store.Expire(now, pathExists, reviewCount(cfg.RepoRoot))

	if os.Getenv("SWATTER_RULES_WRITE") == "1" {
		return writeRuleBook(cfg.RepoRoot, store)
	}
	return nil
}

// reviewCount reads a small counter file so the "compact every N reviews" cadence
// survives across runs. Missing/unreadable → 0 (size-based compaction still
// applies).
func reviewCount(root string) int {
	b, err := os.ReadFile(filepath.Join(root, ".swatter", "review-count"))
	if err != nil {
		return 0
	}
	n := 0
	_, _ = fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &n)
	return n
}

func writeRuleBook(root string, store *RuleStore) error {
	dir := filepath.Join(root, ".swatter")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "rules.md"), []byte(store.Render()), 0o644)
}

// loadRuleBook reads .swatter/rules.md from the repo root, or "" if absent.
func loadRuleBook(root string) string {
	b, err := os.ReadFile(filepath.Join(root, ".swatter", "rules.md"))
	if err != nil {
		return ""
	}
	return string(b)
}

// ExitCode maps a result to a process exit code under the fail_on policy: 0 =
// pass/neutral, 1 = the check should be red. The Action's check-run conclusion
// is derived the same way by the reporter; the exit code is the fallback signal
// for the `run` command outside a check-run context.
func ExitCode(cfg Config, res Result) int {
	for _, f := range res.Findings {
		// Only CONFIRMED findings can fail the build; PLAUSIBLE is advisory.
		if f.Verdict == VerdictConfirmed && cfg.Fails(f.Severity) {
			return 1
		}
	}
	return 0
}
