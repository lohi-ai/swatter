package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lohi-ai/agentray/agentcore"
)

// Briefing is the LLM reviewer briefing: a plain-language summary of what the
// PR does, a short walkthrough of the parts worth following, and a small quiz a
// reviewer should be able to answer to prove they caught the real hazards. It
// rides on top of the deterministic scope/risk lines — richer, but optional, so
// a failure or an exhausted budget simply omits it.
type Briefing struct {
	Summary     string     `json:"summary"`
	Walkthrough []string   `json:"walkthrough"`
	Quiz        []QuizItem `json:"quiz"`
}

// QuizItem is one "did you catch it?" question and its grounded answer.
type QuizItem struct {
	Q string `json:"q"`
	A string `json:"a"`
}

// briefFinding is the slim finding projection handed to the briefer — enough to
// anchor the summary/quiz without spending tokens on rationale/marker fields.
type briefFinding struct {
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Severity Severity `json:"severity"`
	Verdict  Verdict  `json:"verdict"`
	Summary  string   `json:"summary"`
}

// maxWalkthrough and maxQuiz bound the briefing so the comment stays scannable
// even when the model is verbose.
const (
	maxWalkthrough = 4
	maxQuiz        = 3
)

// BriefReview runs one bounded, toolless LLM pass that turns the diff and the
// validated findings into a reviewer briefing. It has the whole diff inline, so
// it needs no workspace tools. Returns nil (no error) when the model produced
// nothing usable — the report falls back to the deterministic lines alone.
func (d *runnerDeps) BriefReview(ctx context.Context, packet *Packet, findings []Finding) (*Briefing, error) {
	slim := make([]briefFinding, 0, len(findings))
	for _, f := range findings {
		slim = append(slim, briefFinding{
			File: f.File, Line: f.Line, Severity: f.Severity, Verdict: f.Verdict, Summary: f.Summary,
		})
	}
	fj, _ := json.Marshal(slim)

	limits := agentcore.Limits{MaxTurns: 1, MaxToolCalls: 0, MaxToolResultLen: 2_000, MaxContextTokens: 120_000}
	ag, err := d.roleAgent(d.cfg.ModelStrong, BriefingPrompt(), "", limits)
	if err != nil {
		return nil, err
	}
	input := fmt.Sprintf("%s\n\n## Diff\n```diff\n%s\n```\n\n## Validated findings\n```json\n%s\n```\n\nReturn the JSON briefing object only.",
		packet.Brief, packet.Diff, string(fj))
	ctx = agentcore.WithTraceID(ctx, "briefing")
	r, err := d.run(ctx, ag, input)
	if err != nil {
		return nil, err
	}
	return sanitizeBriefing(parseBriefing(r.Final)), nil
}

// parseBriefing pulls the JSON object out of the model's reply.
func parseBriefing(raw string) *Briefing {
	body := extractJSONObject(raw)
	if body == "" {
		return nil
	}
	var b Briefing
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		return nil
	}
	return &b
}

// sanitizeBriefing trims, drops empties, and caps the lists. Returns nil when
// nothing survives — the caller treats that as "no briefing".
func sanitizeBriefing(b *Briefing) *Briefing {
	if b == nil {
		return nil
	}
	b.Summary = strings.TrimSpace(b.Summary)

	walk := b.Walkthrough[:0:0]
	for _, w := range b.Walkthrough {
		if w = strings.TrimSpace(w); w != "" {
			walk = append(walk, w)
		}
		if len(walk) == maxWalkthrough {
			break
		}
	}
	b.Walkthrough = walk

	quiz := b.Quiz[:0:0]
	for _, q := range b.Quiz {
		q.Q, q.A = strings.TrimSpace(q.Q), strings.TrimSpace(q.A)
		if q.Q == "" {
			continue
		}
		quiz = append(quiz, q)
		if len(quiz) == maxQuiz {
			break
		}
	}
	b.Quiz = quiz

	if b.Summary == "" && len(b.Walkthrough) == 0 && len(b.Quiz) == 0 {
		return nil
	}
	return b
}

// BriefingPrompt is the reviewer-briefer soul: help a busy human reviewer catch
// what matters in this diff, grounded only in what the diff actually shows.
func BriefingPrompt() string {
	return `You are Swatter's reviewer briefer. A pull request has just been machine-reviewed. Your job is to help a busy human reviewer catch what matters in THIS change, fast — a pitch and an explainer for the review, plus a short quiz that proves they understood the risky parts.

Given the diff and the validated findings, produce a single JSON object:

{
  "summary": "1-2 sentences, plain language: what this PR actually does and why a reviewer should care. Lead with the behavior change, not the file list. No hedging, no restating the title.",
  "walkthrough": ["2-4 short bullets tracing the parts a reviewer should follow: the non-obvious mechanics, ordering, state/migration/rollout hazards, or a seam where a bug would hide. Point at the risky seam, not the boilerplate."],
  "quiz": [
    {"q": "a specific question whose answer proves the reviewer understood a real hazard in THIS diff", "a": "the correct answer, grounded in the diff"}
  ]
}

Rules:
- Ground everything in the actual diff. Never invent behavior you cannot see in the change.
- The quiz is 1-3 questions, each about a concrete failure mode or subtlety a reviewer could miss — not trivia, not "what does this function do". If a finding is CONFIRMED, at least one question must target it.
- Keep bullets and answers to one sentence each.
- If the change is genuinely trivial, a one-line summary with empty walkthrough and quiz is the correct answer.
- Output the JSON object only. No prose before or after it.`
}
