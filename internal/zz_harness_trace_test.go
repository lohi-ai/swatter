package internal

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/lohi-ai/agentray/agentcore"
)

// stubProvider is a stateless, concurrency-safe LLM seam for the offline harness
// trace: it inspects the last user turn and returns a role-appropriate scripted
// answer, so the whole finder→validator→sweep→briefing→lifecycle harness runs
// end-to-end with no network, no key, and no tokens — and every call still flows
// through the TracingProvider so it lands in the JSONL trace. Unlike agentcore's
// FauxProvider it holds no mutable state, so Swatter's parallel finders can hit
// it from many goroutines at once without a data race.
type stubProvider struct{}

func (stubProvider) Name() string        { return "stub" }
func (stubProvider) SupportsTools() bool { return true }

func (stubProvider) Chat(_ context.Context, req agentcore.ChatRequest) (agentcore.ChatResponse, error) {
	return agentcore.AssistantText(scriptFor(lastUser(req))), nil
}

func (stubProvider) Stream(_ context.Context, req agentcore.ChatRequest) (<-chan agentcore.ChatDelta, error) {
	ch := make(chan agentcore.ChatDelta, 2)
	txt := scriptFor(lastUser(req))
	go func() {
		defer close(ch)
		ch <- agentcore.ChatDelta{ContentDelta: txt}
		ch <- agentcore.ChatDelta{Done: true, StopReason: "stop"}
	}()
	return ch, nil
}

func lastUser(req agentcore.ChatRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == agentcore.RoleUser {
			return req.Messages[i].Content
		}
	}
	return ""
}

// scriptFor maps a prompt to the JSON the corresponding role expects. It keys off
// the trailing instruction each phase prompt ends with (see pipeline.go / the
// prompt files), so a finder gets candidates, a validator gets a verdict, etc.
// It matches only the tail of the prompt, because the diff under review can
// itself contain these instruction strings (Swatter reviewing its own prompts).
func scriptFor(full string) string {
	prompt := full
	if len(prompt) > 200 {
		prompt = prompt[len(prompt)-200:]
	}
	switch {
	case strings.Contains(prompt, "JSON verdict object"): // validator
		return `{"verdict":"CONFIRMED","severity":"MAJOR","rationale":"stub: traced the candidate in the real code."}`
	case strings.Contains(prompt, "JSON briefing object"): // briefing
		return `{"summary":"Stub: adds the scope/risk/briefing layer to the review comment.","walkthrough":["stub: threads the packet through both render paths","stub: gates the LLM briefing behind budget"],"quiz":[{"q":"stub: what omits the briefing?","a":"a nil briefing or exhausted budget"}]}`
	case strings.Contains(prompt, "NEW candidates only"): // sweep
		return `[]`
	case strings.Contains(prompt, "generalized rules"): // learn
		return `[]`
	case strings.Contains(prompt, "YES or NO"): // dedup judge
		return "NO"
	case strings.Contains(prompt, "candidates per angle"): // finder
		a := extractAngle(prompt)
		return fmt.Sprintf(`[{"file":"internal/area_%s.go","line":42,"summary":"stub finding from angle %s","failure_scenario":"harness-trace stub scenario","severity":"MAJOR"}]`, a, a)
	default:
		return `[]`
	}
}

// extractAngle pulls the "Your angle(s): X" label out of a finder prompt so each
// angle's stub finding is distinct (→ one validator per angle in the trace).
func extractAngle(prompt string) string {
	const key = "Your angle(s): "
	i := strings.Index(prompt, key)
	if i < 0 {
		return "x"
	}
	rest := prompt[i+len(key):]
	if j := strings.IndexByte(rest, '.'); j >= 0 {
		rest = rest[:j]
	}
	return strings.NewReplacer(", ", "_", " ", "").Replace(strings.TrimSpace(rest))
}

// TestZZHarnessTrace drives the full review harness over THIS repo's own diff
// with the scripted stub provider, writing an agentcore JSONL trace to
// SWATTER_TRACE. It is opt-in (skipped unless SWATTER_TRACE is set) because it
// depends on the working-tree diff and writes a file. Run it with:
//
//	SWATTER_TRACE=/path/trace.jsonl go test ./internal/ -run TestZZHarnessTrace -v
func TestZZHarnessTrace(t *testing.T) {
	tracePath := os.Getenv("SWATTER_TRACE")
	if tracePath == "" {
		t.Skip("set SWATTER_TRACE=<path> to capture the harness trace")
	}
	ctx := context.Background()

	repoRoot := os.Getenv("SWATTER_REPO_ROOT")
	if repoRoot == "" {
		repoRoot = ".."
	}
	base := os.Getenv("SWATTER_BASE")
	if base == "" {
		base = "main"
	}

	packet, err := BuildPacket(ctx, PacketInput{
		RepoRoot: repoRoot,
		BaseRef:  base,
		HeadRef:  "HEAD",
		PRTitle:  "reviewer scope/risk/focus + LLM briefing",
		PRBody:   "Harness-trace run over the working branch.",
	})
	if err != nil {
		t.Fatalf("build packet: %v", err)
	}
	t.Logf("packet: %d changed files, %d changed lines", len(packet.ChangedFiles), packet.ChangedLines)

	cfg := Config{
		Provider:       ProviderAnthropic, // unused: stub overrides the inner provider
		APIKey:         "stub",
		ModelStrong:    "stub-strong",
		ModelCheap:     "stub-cheap",
		MaxUSD:         1000,
		MaxTokensTotal: 1 << 40,
		FailOn:         FailOnNever,
		Briefing:       true,
		RepoRoot:       repoRoot,
	}

	budget := NewBudget(cfg)
	deps, err := newRunnerDeps(cfg, budget) // opens the SWATTER_TRACE sink
	if err != nil {
		t.Fatalf("deps: %v", err)
	}
	deps.inner = stubProvider{} // drive the harness with the scripted seam

	p := &Pipeline{cfg: cfg, deps: deps, packet: packet, progress: func(n string) { t.Logf("progress: %s", n) }}
	res, err := p.Run(ctx)
	if err != nil {
		t.Fatalf("pipeline run: %v", err)
	}
	t.Logf("result: %d findings, sweep=%v, spent $%.2f / %d tok", len(res.Findings), res.SweepRan, res.SpentUSD, res.SpentTokens)

	// Exercise the post-review lifecycle too so learn/dedup calls land in the trace.
	if err := ApplyLifecycle(ctx, cfg, deps, packet, res); err != nil {
		t.Logf("lifecycle (non-fatal): %v", err)
	}
	t.Logf("trace written to %s", tracePath)
}
