package internal

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CmdInit is the one-command onboarding (claude-code-action's install moment):
// it asks the provider, writes .github/workflows/swatter.yml, sets the
// SWATTER_API_KEY secret via `gh`, and prints the branch-protection tip.
func CmdInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	provider := fs.String("provider", "", "anthropic | openai-compat (prompted if empty)")
	model := fs.String("model", "", "strong model id (prompted if empty)")
	baseURL := fs.String("base-url", "", "gateway base URL (openai-compat)")
	mode := fs.String("mode", "", "per-commit | on-demand (prompted if empty)")
	noSecret := fs.Bool("no-secret", false, "skip `gh secret set` (write the workflow only)")
	yes := fs.Bool("yes", false, "accept defaults, no prompts")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	in := bufio.NewReader(os.Stdin)
	ask := func(q, def string) string {
		if *yes {
			return def
		}
		if def != "" {
			fmt.Printf("%s [%s]: ", q, def)
		} else {
			fmt.Printf("%s: ", q)
		}
		line, _ := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		return line
	}

	if *provider == "" {
		*provider = ask("Provider (anthropic | openai-compat)", "anthropic")
	}
	if *model == "" {
		def := "claude-opus-4-8"
		if Provider(*provider) == ProviderOpenAICompat {
			def = ""
		}
		*model = ask("Strong model id", def)
	}
	if Provider(*provider) == ProviderOpenAICompat && *baseURL == "" {
		*baseURL = ask("Gateway base URL (e.g. https://9router.example/v1)", "")
	}
	if *mode == "" {
		fmt.Println("When should Swatter review?")
		fmt.Println("  per-commit — on every push to the PR (reviews continuously)")
		fmt.Println("  on-demand  — on PR open, then only when a maintainer comments \"@swatter review\" (saves tokens)")
		*mode = ask("Review trigger (per-commit | on-demand)", "per-commit")
	}
	if *mode != string(modePerCommit) && *mode != string(modeOnDemand) {
		fmt.Fprintf(os.Stderr, "swatter init: unknown mode %q (want on-demand | per-commit)\n", *mode)
		return 2
	}

	wf := renderWorkflow(*provider, *model, *baseURL, ReviewMode(*mode))
	dst := filepath.Join(".github", "workflows", "swatter.yml")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "swatter init: %v\n", err)
		return 1
	}
	if err := os.WriteFile(dst, []byte(wf), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "swatter init: %v\n", err)
		return 1
	}
	fmt.Printf("✓ wrote %s\n", dst)

	if !*noSecret {
		if err := setSecret(in, *yes); err != nil {
			fmt.Fprintf(os.Stderr, "! could not set SWATTER_API_KEY: %v\n", err)
			fmt.Println("  Set it manually: Settings → Secrets → Actions → SWATTER_API_KEY")
		}
	}

	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Commit .github/workflows/swatter.yml and open a PR — Swatter reviews it.")
	if ReviewMode(*mode) == modeOnDemand {
		fmt.Println("  2. Push more commits freely — Swatter stays quiet. Comment \"@swatter review\"")
		fmt.Println("     (as a repo owner/member/collaborator) to re-review the latest head.")
		fmt.Println("  3. (optional) Require the \"Swatter\" check under Settings → Branches →")
		fmt.Println("     Branch protection to block merges on confirmed findings.")
	} else {
		fmt.Println("  2. (optional) Require the \"Swatter\" check under Settings → Branches →")
		fmt.Println("     Branch protection to block merges on confirmed findings.")
	}
	return 0
}

// setSecret shells out to `gh secret set SWATTER_API_KEY`, reading the value
// from stdin without echoing it into the workflow or logs.
func setSecret(in *bufio.Reader, yes bool) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI not found")
	}
	if yes {
		return fmt.Errorf("skipped in --yes mode; set SWATTER_API_KEY manually")
	}
	fmt.Print("Paste your BYOK API key (stored as the SWATTER_API_KEY secret, not echoed to the workflow): ")
	key, _ := in.ReadString('\n')
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("empty key")
	}
	cmd := exec.Command("gh", "secret", "set", "SWATTER_API_KEY")
	cmd.Stdin = strings.NewReader(key)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	fmt.Println("✓ set SWATTER_API_KEY secret")
	return nil
}

// ReviewMode selects when the generated workflow triggers a review.
type ReviewMode string

const (
	// modeOnDemand reviews on PR open/reopen, then only when a maintainer
	// comments "@swatter review" — no per-commit runs, so it spends far fewer
	// tokens on churny PRs.
	modeOnDemand ReviewMode = "on-demand"
	// modePerCommit reviews on every push (opened + synchronize) — continuous
	// but pays for a full review per commit.
	modePerCommit ReviewMode = "per-commit"
)

func renderWorkflow(provider, model, baseURL string, mode ReviewMode) string {
	with := "          api_key: ${{ secrets.SWATTER_API_KEY }}\n"
	if provider != "" && provider != string(ProviderAnthropic) {
		with += fmt.Sprintf("          provider: %s\n", provider)
	}
	if baseURL != "" {
		with += fmt.Sprintf("          base_url: %s\n", baseURL)
	}
	if model != "" {
		with += fmt.Sprintf("          model: %s\n", model)
	}
	if mode == modeOnDemand {
		return onDemandWorkflow + with
	}
	return perCommitWorkflow + with
}

// perCommitWorkflow reviews on every push. `closed` runs the post-merge
// feedback/learn flow (merged PRs only) and needs contents:write so the rule
// book can be committed to the base branch.
const perCommitWorkflow = `name: swatter
on:
  pull_request:
    types: [opened, synchronize, reopened, closed]

concurrency:
  group: swatter-${{ github.event.pull_request.number }}
  cancel-in-progress: true

permissions:
  contents: write
  pull-requests: write
  checks: write

jobs:
  review:
    # Same-repo PRs only: on the pull_request event a fork PR gets a read-only
    # token and no secrets, so an auto-review can't post — skip it instead of
    # burning a runner that exits neutral. (closed still runs the post-merge
    # learn flow.) To review fork PRs on a public repo, use on-demand mode,
    # where a maintainer's "@swatter review" comment runs with a write token.
    if: >-
      github.event.action == 'closed' ||
      github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - id: swatter
        uses: lohi-ai/swatter@v0
        with:
`

// onDemandWorkflow reviews on PR open/reopen and on a "@swatter review" comment
// from a trusted commenter (OWNER/MEMBER/COLLABORATOR) — not on every push. The
// auto-run is gated to same-repo PRs; fork PRs (read-only token, no secrets on
// pull_request) don't auto-review but a maintainer's "@swatter review" comment
// reviews them with a write token. The comment payload has no PR head, so it
// checks out refs/pull/N/head. `closed` still runs the post-merge learn flow.
// The concurrency group falls back to the issue number on comment events (both
// resolve to the PR number).
const onDemandWorkflow = `name: swatter
on:
  pull_request:
    types: [opened, reopened, closed]
  issue_comment:
    types: [created]

concurrency:
  group: swatter-${{ github.event.issue.number || github.event.pull_request.number }}
  cancel-in-progress: true

permissions:
  contents: write
  pull-requests: write
  checks: write

jobs:
  review:
    if: >-
      (github.event_name == 'pull_request' &&
       (github.event.action == 'closed' ||
        github.event.pull_request.head.repo.full_name == github.repository)) ||
      (github.event.issue.pull_request &&
       contains(github.event.comment.body, '@swatter review') &&
       contains(fromJSON('["OWNER","MEMBER","COLLABORATOR"]'), github.event.comment.author_association))
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
          ref: ${{ github.event_name == 'issue_comment' && format('refs/pull/{0}/head', github.event.issue.number) || '' }}
      - id: swatter
        uses: lohi-ai/swatter@v0
        with:
`
