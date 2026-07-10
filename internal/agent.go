package internal

import (
	"context"
	"fmt"

	"github.com/lohi-ai/agentray/agentcore"
	"github.com/lohi-ai/agentray/sandbox"
)

// runnerDeps are the shared, per-review dependencies every phase run needs: the
// provider factory, the read-only workspace, and the budget ledger.
type runnerDeps struct {
	cfg    Config
	budget *Budget
	ws     *sandbox.Workspace
}

// newRunnerDeps wires the provider-independent pieces once per review. The
// workspace is the Action checkout, guarded read-only.
func newRunnerDeps(cfg Config, budget *Budget) (*runnerDeps, error) {
	ws, err := sandbox.NewWorkspace(cfg.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("workspace at %q: %w", cfg.RepoRoot, err)
	}
	return &runnerDeps{cfg: cfg, budget: budget, ws: ws}, nil
}

// provider builds the BYOK provider for a model. anthropic uses the native API;
// openai-compat targets any OpenAI-wire gateway via BaseURL, with tool support
// assumed (finders/validators don't require tools — they read via the toolset,
// but a compat server that rejects the tools field would degrade gracefully).
func (d *runnerDeps) provider() agentcore.LLMProvider {
	switch d.cfg.Provider {
	case ProviderOpenAICompat:
		compat := agentcore.DefaultCompat()
		return agentcore.NewOpenAIProvider(d.cfg.APIKey, d.cfg.BaseURL, compat)
	default:
		return agentcore.NewAnthropicProvider(d.cfg.APIKey, "")
	}
}

// readOnlyTools builds a fresh toolset rooted at the checkout: read_file, grep,
// glob. No shell, no write, no network — the injection posture that lets us run
// attacker-controlled PR diffs safely (the agent can only read the repo).
func (d *runnerDeps) readOnlyTools() *agentcore.ToolSet {
	return agentcore.NewToolSet(
		sandbox.NewReadFileTool(d.ws),
		sandbox.NewGrepTool(d.ws),
		sandbox.NewGlobTool(d.ws),
	)
}

// roleAgent constructs a bounded agentcore.Agent for one phase run. soul is the
// always-loaded preamble, agents is the role/angle charter (both <8KB per the
// AgentDefinition budget). model picks the tier. The budget gate is shared
// across the whole review via the ledger.
func (d *runnerDeps) roleAgent(model, soul, agents string, limits agentcore.Limits) (*agentcore.Agent, error) {
	return agentcore.New(agentcore.Config{
		Provider: d.provider(),
		Model:    model,
		Tools:    d.readOnlyTools(),
		Policy:   agentcore.NewAllowList(sandbox.ToolReadFile, sandbox.ToolGrep, sandbox.ToolGlob),
		Definition: agentcore.AgentDefinition{
			Soul:   soul,
			Agents: agents,
		},
		Limits:     &limits,
		BudgetGate: d.budget.Gate(),
		// Deterministic caps keep per-PR cost bounded; a finder reads the diff
		// and a handful of enclosing functions, not the whole tree.
		MaxTokens: 8192,
	})
}

// run executes a role agent on an input prompt, commits its usage to the shared
// ledger, and returns the final text.
func (d *runnerDeps) run(ctx context.Context, ag *agentcore.Agent, input string) (agentcore.RunResult, error) {
	res, err := ag.Prompt(ctx, input)
	// Commit whatever was spent even on error — a failed run still cost tokens.
	d.budget.Commit(res.Usage)
	return res, err
}
