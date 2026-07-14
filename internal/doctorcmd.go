package internal

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/lohi-ai/agentray/agentcore"
)

// CmdDoctor verifies a standalone setup before a full review burns tokens:
// config validity, git context, GitHub token access, and one cheap model
// round-trip against the configured provider. It exits non-zero when a critical
// check (config invalid, or the model round-trip fails) fails; warnings
// (e.g. no GitHub token) do not fail it, since local review still works.
func CmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	noLLM := fs.Bool("no-llm", false, "skip the model round-trip (offline check)")
	repoRoot := fs.String("repo-root", "", "checkout root (default: .)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx := context.Background()
	ok := true
	pass := func(msg string) { fmt.Printf("  ✓ %s\n", msg) }
	fail := func(msg string) { ok = false; fmt.Printf("  ✗ %s\n", msg) }
	warn := func(msg string) { fmt.Printf("  · %s\n", msg) }

	fmt.Println("swatter doctor")

	// (a) config
	cfg, err := LoadConfig()
	if err != nil {
		fail(fmt.Sprintf("config: %v", err))
		fmt.Println("\ndoctor: config invalid — fix it (`swatter config set …`) before reviewing.")
		return 1
	}
	if *repoRoot != "" {
		cfg.RepoRoot = *repoRoot
	}
	pass(fmt.Sprintf("config ok — provider=%s model=%s api-key=set", cfg.Provider, cfg.ModelStrong))
	if cfg.Provider == ProviderOpenAICompat {
		pass("base-url=" + cfg.BaseURL)
	}

	// Resolve the origin remote once — both the git-context check and the token
	// check need owner/repo, and each lookup shells out to git.
	owner, repo, originErr := originRepo(ctx, cfg.RepoRoot)

	// (b) git context
	if _, gerr := git(ctx, cfg.RepoRoot, "rev-parse", "--is-inside-work-tree"); gerr != nil {
		warn("not a git repo — `swatter review` needs a git checkout")
	} else {
		db := defaultBranch(ctx, cfg.RepoRoot)
		if originErr == nil {
			pass(fmt.Sprintf("git ok — default branch %s, origin %s/%s", db, owner, repo))
		} else {
			warn(fmt.Sprintf("git ok — default branch %s, but origin not resolvable (%v); --comment needs a GitHub remote", db, originErr))
		}
	}

	// (c) GitHub token
	if originErr == nil {
		gh, cerr := NewGitHubClientForRepo(owner, repo)
		switch {
		case cerr != nil:
			fail(fmt.Sprintf("github client: %v", cerr))
		case gh == nil:
			warn("no GitHub token — local review works; `review --comment` will not (set SWATTER_GITHUB_TOKEN or `swatter config set github-token …`)")
		default:
			for _, line := range gh.PreflightTokens(ctx).Render() {
				fmt.Printf("    %s\n", line)
			}
		}
	}

	// (d) model round-trip
	if *noLLM {
		warn("model round-trip skipped (--no-llm)")
	} else if rt, terr := modelRoundTrip(ctx, cfg); terr != nil {
		fail(fmt.Sprintf("model round-trip (%s): %v", cfg.ModelCheap, terr))
	} else {
		pass(fmt.Sprintf("model round-trip ok (%s) in %s", cfg.ModelCheap, rt.Round(time.Millisecond)))
	}

	fmt.Println()
	if !ok {
		fmt.Println("doctor: one or more checks failed.")
		return 1
	}
	fmt.Println("doctor: ready — try `swatter review`.")
	return 0
}

// modelRoundTrip does one cheap completion against the configured BYOK provider
// to prove the key/gateway/model actually answer, and returns the latency.
func modelRoundTrip(ctx context.Context, cfg Config) (time.Duration, error) {
	deps, err := newRunnerDeps(cfg, NewBudget(cfg))
	if err != nil {
		return 0, err
	}
	start := time.Now()
	_, err = deps.provider().Chat(ctx, agentcore.ChatRequest{
		Model:     cfg.ModelCheap,
		Messages:  []agentcore.Message{{Role: agentcore.RoleUser, Content: "ping"}},
		MaxTokens: 8,
	})
	if err != nil {
		return 0, err
	}
	return time.Since(start), nil
}
