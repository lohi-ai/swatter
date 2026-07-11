package internal

import (
	"context"
	"sync"

	"github.com/lohi-ai/agentray/agentcore"
)

// Budget is the shared atomic cost/token ledger across every phase run. Each
// agentcore.Agent carries its own per-run BudgetGate, but the finders and
// validators run in parallel and in sequence against ONE ceiling — so they must
// all debit a single counter. Committed usage from finished runs accumulates;
// the gate for an in-flight run compares committed + that run's live usage
// against the caps.
//
// Two backstops, because agentcore pricing only knows published models:
//   - maxUSD fires when priced spend crosses the dollar cap (using the user's
//     price override when the model is a custom gateway id agentcore prices $0).
//   - maxTokens is the always-works ceiling for unknown-price models.
type Budget struct {
	mu              sync.Mutex
	committedUSD    float64
	committedTok    int
	maxUSD          float64
	maxTokens       int
	pricePerMTokIn  float64
	pricePerMTokOut float64
}

// NewBudget builds a ledger from the resolved config caps.
func NewBudget(c Config) *Budget {
	return &Budget{
		maxUSD:          c.MaxUSD,
		maxTokens:       c.MaxTokensTotal,
		pricePerMTokIn:  c.PricePerMTokIn,
		pricePerMTokOut: c.PricePerMTokOut,
	}
}

// effectiveCost returns the greater of agentcore's priced cost and the cost
// computed from the user's per-MTok override, so a custom gateway model that
// agentcore prices at $0 still meters against maxUSD when the user supplies a
// price.
func (b *Budget) effectiveCost(u agentcore.Usage) float64 {
	cost := u.CostUSD
	if b.pricePerMTokIn > 0 || b.pricePerMTokOut > 0 {
		override := float64(u.InputTokens+u.CacheReadTokens+u.CacheWriteTokens)/1_000_000*b.pricePerMTokIn +
			float64(u.OutputTokens)/1_000_000*b.pricePerMTokOut
		if override > cost {
			cost = override
		}
	}
	return cost
}

func tokens(u agentcore.Usage) int {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// Gate returns a per-run agentcore BudgetGate closure. It stops the run once
// this run's own usage reaches runGateTokens (the per-agent effort cap's
// wind-down threshold — see gateTokens; 0 fires immediately, negative means
// uncapped), or once committed spend (from finished runs) plus this run's live
// usage crosses either shared cap.
func (b *Budget) Gate(runGateTokens int) func(ctx context.Context, u agentcore.Usage) bool {
	return func(_ context.Context, u agentcore.Usage) bool {
		if runGateTokens >= 0 && tokens(u) >= runGateTokens {
			return true
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		projUSD := b.committedUSD + b.effectiveCost(u)
		projTok := b.committedTok + tokens(u)
		if b.maxUSD > 0 && projUSD >= b.maxUSD {
			return true
		}
		if b.maxTokens > 0 && projTok >= b.maxTokens {
			return true
		}
		return false
	}
}

// Commit folds a finished run's usage into the shared totals.
func (b *Budget) Commit(u agentcore.Usage) {
	b.mu.Lock()
	b.committedUSD += b.effectiveCost(u)
	b.committedTok += tokens(u)
	b.mu.Unlock()
}

// Exhausted reports whether either cap is already crossed by committed spend,
// so the pipeline can stop launching new phases.
func (b *Budget) Exhausted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.maxUSD > 0 && b.committedUSD >= b.maxUSD {
		return true
	}
	if b.maxTokens > 0 && b.committedTok >= b.maxTokens {
		return true
	}
	return false
}

// Spent returns the committed dollars and tokens for reporting.
func (b *Budget) Spent() (usd float64, tok int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.committedUSD, b.committedTok
}
