package internal

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Provider selects the inference backend. BYOK is the only mode: anthropic uses
// the native Anthropic API; openai-compat targets any OpenAI-wire gateway
// (9router, OpenRouter, LiteLLM, Ollama, …) via BaseURL.
type Provider string

const (
	ProviderAnthropic    Provider = "anthropic"
	ProviderOpenAICompat Provider = "openai-compat"
)

// FailOn maps the worst surviving finding to the check-run conclusion.
type FailOn string

const (
	FailOnCritical FailOn = "critical" // red only on a CRITICAL
	FailOnMajor    FailOn = "major"    // red on MAJOR or worse
	FailOnAny      FailOn = "any"      // red on any finding, incl. MINOR
	FailOnNever    FailOn = "never"    // advisory: green check + comments (default)
)

// Config is the fully-resolved run configuration. Built from environment
// variables (the Action maps every input to SWATTER_* env) so the same struct
// serves the Action, `swatter serve` (phase 2), and tests.
type Config struct {
	Provider Provider
	APIKey   string
	BaseURL  string // openai-compat only

	// Model tiers. Strong runs bug/security angles (A–D) and every angle on a
	// large diff; Cheap may run E–G a tier down on a small diff (review-pr §2).
	ModelStrong string
	ModelCheap  string

	// Budget backstops. MaxUSD uses agentcore pricing (known models only);
	// MaxTokensTotal is the always-works ceiling for unknown gateway models
	// whose per-token price agentcore can't look up. PricePerMTokIn/Out let a
	// user teach the ledger a custom model's price so MaxUSD fires for it too.
	MaxUSD         float64
	MaxTokensTotal int
	PricePerMTokIn float64
	PricePerMTokOut float64

	FailOn FailOn

	// RepoRoot is the checkout the read-only toolset is rooted at.
	RepoRoot string

	// Feedback/learn flow (post-merge). PromoteAfter is the observation weight a
	// same-pattern cluster must accumulate before it becomes a rule (a missed-bug
	// observation weighs 2, a repeat-finding 1). RulesCommit gates the post-merge
	// Contents-API commit of .swatter/{rules,pending}.md to the base branch.
	PromoteAfter int
	RulesCommit  bool
}

// LoadConfig resolves a Config from the SWATTER_* environment the Action sets.
// It validates the minimum required inputs (api key, a strong model) and
// applies the documented defaults.
func LoadConfig() (Config, error) {
	c := Config{
		Provider:       Provider(envDefault("SWATTER_PROVIDER", string(ProviderAnthropic))),
		APIKey:         os.Getenv("SWATTER_API_KEY"),
		BaseURL:        os.Getenv("SWATTER_BASE_URL"),
		ModelStrong:    os.Getenv("SWATTER_MODEL"),
		ModelCheap:     os.Getenv("SWATTER_MODEL_CHEAP"),
		MaxUSD:         envFloat("SWATTER_MAX_USD", 5.0),
		MaxTokensTotal: envInt("SWATTER_MAX_TOKENS_TOTAL", 8_000_000),
		PricePerMTokIn: envFloat("SWATTER_PRICE_PER_MTOK_IN", 0),
		PricePerMTokOut: envFloat("SWATTER_PRICE_PER_MTOK_OUT", 0),
		FailOn:         FailOn(envDefault("SWATTER_FAIL_ON", string(FailOnNever))),
		RepoRoot:       envDefault("SWATTER_REPO_ROOT", "."),
		PromoteAfter:   envInt("SWATTER_RULE_PROMOTE_AFTER", 3),
		RulesCommit:    envBool("SWATTER_RULES_COMMIT", true),
	}

	// A strong model is mandatory. If SWATTER_MODEL is unset, fall back to a
	// sensible per-provider default so `swatter run` works with only a key.
	if c.ModelStrong == "" {
		c.ModelStrong = defaultModel(c.Provider)
	}
	// Cheap defaults to strong: correctness over cost when unspecified.
	if c.ModelCheap == "" {
		c.ModelCheap = c.ModelStrong
	}

	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	switch c.Provider {
	case ProviderAnthropic, ProviderOpenAICompat:
	default:
		return fmt.Errorf("unknown provider %q (want anthropic | openai-compat)", c.Provider)
	}
	if c.APIKey == "" {
		return fmt.Errorf("SWATTER_API_KEY is required (BYOK)")
	}
	if c.Provider == ProviderOpenAICompat && c.BaseURL == "" {
		return fmt.Errorf("SWATTER_BASE_URL is required for provider openai-compat")
	}
	switch c.FailOn {
	case FailOnCritical, FailOnMajor, FailOnAny, FailOnNever:
	default:
		return fmt.Errorf("unknown fail_on %q (want critical | major | any | never)", c.FailOn)
	}
	return nil
}

// Fails reports whether a surviving finding of the given severity should turn
// the check run red under this config's fail_on policy.
func (c Config) Fails(sev Severity) bool {
	switch c.FailOn {
	case FailOnNever:
		return false
	case FailOnAny:
		return true
	case FailOnMajor:
		return severityRank(sev) >= severityRank(SevMajor)
	case FailOnCritical:
		return severityRank(sev) >= severityRank(SevCritical)
	default:
		return false
	}
}

func defaultModel(p Provider) string {
	switch p {
	case ProviderAnthropic:
		return "claude-opus-4-8"
	default:
		// openai-compat: no universal default; require the user to name it.
		return ""
	}
}

func envDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
