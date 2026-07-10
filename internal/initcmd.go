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

	wf := renderWorkflow(*provider, *model, *baseURL)
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
	fmt.Println("  2. (optional) Require the \"Swatter\" check under Settings → Branches →")
	fmt.Println("     Branch protection to block merges on confirmed findings.")
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

func renderWorkflow(provider, model, baseURL string) string {
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
	return `name: swatter
on:
  pull_request:
    types: [opened, synchronize, reopened]

concurrency:
  group: swatter-${{ github.event.pull_request.number }}
  cancel-in-progress: true

permissions:
  contents: read
  pull-requests: write
  checks: write

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - id: swatter
        uses: lohi-ai/swatter@v0
        with:
` + with
}
