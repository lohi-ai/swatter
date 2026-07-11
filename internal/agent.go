package internal

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/lohi-ai/agentray/agentcore"
	"github.com/lohi-ai/agentray/sandbox"
)

// runnerDeps are the shared, per-review dependencies every phase run needs: the
// provider factory, the read-only workspace, and the budget ledger.
type runnerDeps struct {
	cfg    Config
	budget *Budget
	ws     *sandbox.Workspace
	// sink, when non-nil (SWATTER_TRACE set), receives one TraceRecord per LLM
	// call — the request messages, response, tokens, cost, and latency — so a
	// run's harness can be reviewed after the fact.
	sink agentcore.TraceSink
	// inner overrides the BYOK provider. nil in production (built from cfg); set
	// by tests to drive the harness with a scripted provider and no network.
	inner agentcore.LLMProvider
}

// newRunnerDeps wires the provider-independent pieces once per review. The
// workspace is the Action checkout, guarded read-only. SWATTER_TRACE=<path>
// opens a JSONL trace sink for every LLM call this review makes.
func newRunnerDeps(cfg Config, budget *Budget) (*runnerDeps, error) {
	ws, err := sandbox.NewWorkspace(cfg.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("workspace at %q: %w", cfg.RepoRoot, err)
	}
	d := &runnerDeps{cfg: cfg, budget: budget, ws: ws}
	if path := strings.TrimSpace(os.Getenv("SWATTER_TRACE")); path != "" {
		if sink, err := agentcore.NewFileTraceSink(path); err == nil {
			d.sink = sink
		}
	}
	return d, nil
}

// provider builds the review provider: the BYOK inner provider (or a test
// override), transparently wrapped with agentcore's TracingProvider when a
// trace sink is configured so every call is priced and recorded.
func (d *runnerDeps) provider() agentcore.LLMProvider {
	inner := d.inner
	if inner == nil {
		inner = d.buildProvider()
	}
	if d.sink == nil {
		return inner
	}
	return agentcore.NewTracingProvider(inner, agentcore.DefaultPricing(), d.sink)
}

// buildProvider builds the BYOK provider for a model. anthropic uses the native
// API; openai-compat targets any OpenAI-wire gateway via BaseURL, with tool
// support assumed (finders/validators don't require tools — they read via the
// toolset, but a compat server that rejects the tools field would degrade
// gracefully).
func (d *runnerDeps) buildProvider() agentcore.LLMProvider {
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
	// A toolless phase (MaxToolCalls == 0, e.g. the briefing, which has the whole
	// diff inline) must not advertise the read-only toolset: the model can never
	// call it, so the schemas are dead request tokens that only invite a rejected
	// tool call.
	var tools *agentcore.ToolSet
	var policy agentcore.Policy
	if limits.MaxToolCalls != 0 {
		tools = d.readOnlyTools()
		policy = agentcore.NewAllowList(sandbox.ToolReadFile, sandbox.ToolGrep, sandbox.ToolGlob)
	}
	return agentcore.New(agentcore.Config{
		Provider: d.provider(),
		Model:    model,
		Tools:    tools,
		Policy:   policy,
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
