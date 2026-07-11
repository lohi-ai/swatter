package internal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"time"

	"github.com/lohi-ai/agentray/agentcore"
	"github.com/lohi-ai/agentray/sandbox"
)

// runnerDeps are the shared, per-review dependencies every phase run needs: the
// provider factory, the read-only workspace, and the budget ledger.
type runnerDeps struct {
	cfg    Config
	budget *Budget
	ws     *sandbox.Workspace
	// reviewID salts every role agent's prompt-cache key so this review's cached
	// prefixes never collide with another run's (and a gateway can route each
	// agent's turns to the same cache shard).
	reviewID string
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
	d := &runnerDeps{cfg: cfg, budget: budget, ws: ws, reviewID: newReviewID()}
	if path := strings.TrimSpace(os.Getenv("SWATTER_TRACE")); path != "" {
		if sink, err := agentcore.NewFileTraceSink(path); err == nil {
			d.sink = sink
		}
	}
	return d, nil
}

// newReviewID returns a short random id used to salt prompt-cache keys. On the
// (never-seen) chance the OS entropy read fails, a time-based fallback keeps the
// key unique enough — caching is an optimization, not a correctness concern.
func newReviewID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
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
	prof := d.cfg.EffortProfile()
	return agentcore.New(agentcore.Config{
		Provider: d.provider(),
		Model:    model,
		Tools:    tools,
		Policy:   policy,
		// Set only at effort max — the one knob separating max from xhigh.
		// Providers without a thinking-effort parameter ignore it.
		ReasoningEffort: prof.ReasoningEffort,
		Definition: agentcore.AgentDefinition{
			Soul:   soul,
			Agents: agents,
		},
		Limits: &limits,
		// The gate holds this run under the effort level's per-agent token cap
		// (wind-down starts early enough that the remaining turns fit — see
		// gateTokens) and the review-wide ledger caps.
		BudgetGate: d.budget.Gate(gateTokens(prof.PerAgentTokens, limits)),
		// Every phase opts into provider prompt caching: an agent loop re-sends
		// its whole transcript each turn, and the stable prefix (system + brief +
		// diff) dwarfs each turn's delta — a finder's 18-turn run re-bills a ~30K
		// prefix 18× without it. Anthropic gets cache_control breakpoints; an
		// OpenAI-wire gateway gets prompt_cache_key routing. The key is unique per
		// review and per role so agents never contend for one cache shard.
		PromptCacheKey: fmt.Sprintf("swatter-%s-%s", d.reviewID, roleHash(model, soul, agents)),
		// Deterministic caps keep per-PR cost bounded; a finder reads the diff
		// and a handful of enclosing functions, not the whole tree. The constant
		// is part of the per-agent budget math in gateTokens.
		MaxTokens: maxOutputTokens,
	})
}

// roleHash fingerprints a role (model + prompts) for the prompt-cache key, so
// each distinct agent in the review gets its own cache identity.
func roleHash(model, soul, agents string) string {
	h := fnv.New32a()
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(soul))
	h.Write([]byte{0})
	h.Write([]byte(agents))
	return fmt.Sprintf("%08x", h.Sum32())
}

// run executes a role agent on an input prompt, commits its usage to the shared
// ledger, and returns the final text.
func (d *runnerDeps) run(ctx context.Context, ag *agentcore.Agent, input string) (agentcore.RunResult, error) {
	res, err := ag.Prompt(ctx, input)
	// Commit whatever was spent even on error — a failed run still cost tokens.
	d.budget.Commit(res.Usage)
	return res, err
}

// runContinue resumes a role agent from a prior run's transcript (its working
// memory) with a new task, committing usage like run. Used to salvage a phase
// that hit its turn/tool ceiling before emitting its final answer: we replay
// what it already gathered through a fresh, toolless agent and ask it to
// conclude. task also seeds skill/memory recall.
func (d *runnerDeps) runContinue(ctx context.Context, ag *agentcore.Agent, history []agentcore.Message, task string) (agentcore.RunResult, error) {
	res, err := ag.Continue(ctx, history, task)
	d.budget.Commit(res.Usage)
	return res, err
}
