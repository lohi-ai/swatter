package internal

import (
	"encoding/json"
	"strings"
)

// This file is the post-merge feedback read-back: it turns human signals on a
// merged PR (reactions, replies, resolved threads, lines changed before merge,
// and bugs other reviewers caught) into the rule lifecycle's existing inputs —
// hit/miss rule ids for Score, plus pending observations that may later be
// promoted into new rules. Everything here is deterministic and pure; the LLM
// steps (clustering, dedup) live in feedback_llm.go.

// --- finding marker ---
//
// Each inline comment swatter posts embeds an invisible HTML comment carrying
// the finding's rule ids and summary, so the feedback pass can map a comment
// back to the rules that produced it without guessing from rendered prose.
// json.Marshal escapes '>' (>), so a summary can never contain the "-->"
// terminator and break out of the marker.

const findingMarkerPrefix = "<!-- swatter:finding "

type findingMarker struct {
	RuleIDs  []string `json:"rule_ids,omitempty"`
	Severity Severity `json:"severity,omitempty"`
	Summary  string   `json:"summary,omitempty"`
}

func renderFindingMarker(f Finding) string {
	b, err := json.Marshal(findingMarker{RuleIDs: f.RuleIDs, Severity: f.Severity, Summary: f.Summary})
	if err != nil {
		return ""
	}
	return findingMarkerPrefix + string(b) + " -->"
}

// parseFindingMarker extracts the marker from a comment body. ok=false when the
// body carries none — i.e. the comment is not one of swatter's inline findings.
func parseFindingMarker(body string) (findingMarker, bool) {
	start := strings.Index(body, findingMarkerPrefix)
	if start < 0 {
		return findingMarker{}, false
	}
	rest := body[start+len(findingMarkerPrefix):]
	end := strings.Index(rest, "-->")
	if end < 0 {
		return findingMarker{}, false
	}
	var m findingMarker
	if err := json.Unmarshal([]byte(strings.TrimSpace(rest[:end])), &m); err != nil {
		return findingMarker{}, false
	}
	return m, true
}

// --- feedback analysis ---

// PRFeedback is what one merged PR's comment history says about the rule book.
type PRFeedback struct {
	HitRuleIDs  []string // rules whose findings got positive human feedback
	MissRuleIDs []string // rules whose findings humans marked as noise
	// Observations to append to the pending ledger: positively-received swatter
	// findings that no existing rule produced (kind=repeat), and actioned bug
	// reports by other reviewers that swatter missed (kind=missed).
	Observations []Observation
	// SwatterComments / Signals summarize coverage for the progress note.
	SwatterComments int
	Signals         int
}

// AnalyzeFeedback classifies every swatter inline comment on a merged PR.
// swatterLogin is the GitHub account swatter posts as (github-actions[bot] under
// the default token); resolved maps review-comment id → thread-resolved (may be
// nil: no data).
//
// A comment is only treated as one of swatter's findings when it BOTH carries
// the finding marker AND is authored by swatterLogin — the marker is public
// text any participant can copy, so trusting it alone would let a reviewer forge
// feedback that scores arbitrary rules. A marker on a non-swatter comment is
// ignored; a swatter comment without a marker (e.g. one predating the marker) is
// skipped rather than mistaken for another reviewer's missed bug.
//
// Signal model, most explicit wins:
//   - explicit: 👍/👎 reactions on the comment plus classified replies
//     ("fixed", "good catch" vs "false positive", "not a bug"); net > 0 is a
//     hit, net < 0 a miss.
//   - implicit (only when explicit signals tie at zero): a resolved thread or
//     an outdated anchor (the flagged line was changed before merge) is a weak
//     hit — the finding was very likely acted on.
//   - nothing → no signal; silence never decays a rule.
func AnalyzeFeedback(pr int, date, swatterLogin string, comments []ReviewCommentData, resolved map[int64]bool) PRFeedback {
	var fb PRFeedback

	replies := map[int64][]ReviewCommentData{}
	for _, c := range comments {
		if c.InReplyToID != 0 {
			replies[c.InReplyToID] = append(replies[c.InReplyToID], c)
		}
	}

	fromSwatter := func(c ReviewCommentData) bool {
		return swatterLogin != "" && strings.EqualFold(c.User.Login, swatterLogin)
	}

	for _, c := range comments {
		if c.InReplyToID != 0 {
			continue // threads are scored at their root
		}
		marker, hasMarker := parseFindingMarker(c.Body)
		if hasMarker {
			// Only score a marked comment that genuinely came from swatter. A
			// forged marker from any other author is ignored — neither scored as
			// a hit/miss nor counted as a missed bug.
			if !fromSwatter(c) {
				continue
			}
			fb.SwatterComments++
			sig := classifyThread(c, replies[c.ID], resolved)
			if sig == 0 {
				continue
			}
			fb.Signals++
			switch {
			case sig > 0 && len(marker.RuleIDs) > 0:
				fb.HitRuleIDs = append(fb.HitRuleIDs, marker.RuleIDs...)
			case sig > 0 && marker.Summary != "":
				// A finding humans confirmed valuable that no rule produced: a
				// repeat of this pattern is evidence the book needs a rule.
				fb.Observations = append(fb.Observations, Observation{
					Kind: ObsRepeat, PR: pr, Date: date, Path: c.Path, Note: oneLine(marker.Summary),
				})
			case sig < 0:
				fb.MissRuleIDs = append(fb.MissRuleIDs, marker.RuleIDs...)
			}
			continue
		}

		// No marker. A markerless swatter comment (e.g. backfilled/pre-marker) is
		// its own finding, not another reviewer's — skip it, don't score it as a
		// missed bug.
		if fromSwatter(c) {
			continue
		}

		// A root comment by someone else (human or another bot). If it was acted
		// on — line changed before merge, thread resolved, or an affirming reply —
		// it likely caught a real problem swatter missed.
		if missedBugSignal(c, replies[c.ID], resolved) {
			fb.Observations = append(fb.Observations, Observation{
				Kind: ObsMissed, PR: pr, Date: date, Path: c.Path, Note: oneLine(c.Body),
			})
		}
	}
	return fb
}

// classifyThread scores one swatter comment thread: >0 hit, <0 miss, 0 none.
func classifyThread(root ReviewCommentData, replies []ReviewCommentData, resolved map[int64]bool) int {
	net := root.Reactions.Up - root.Reactions.Down
	for _, r := range replies {
		if _, own := parseFindingMarker(r.Body); own {
			continue // never score swatter's own text
		}
		net += classifyReply(r.Body)
	}
	if net != 0 {
		return net
	}
	if resolved[root.ID] || root.Outdated() {
		return 1
	}
	return 0
}

// classifyReply maps a human reply to -1 (rejects the finding), +1 (confirms
// it), or 0. Negative phrases are checked first: they are the more specific
// ("incorrect" must not match "correct", "false positive" must not read as
// positive), and when a reply mixes both, decaying a noisy rule is the safer
// default.
func classifyReply(body string) int {
	t := strings.ToLower(body)
	for _, kw := range negativeReplyPhrases {
		if strings.Contains(t, kw) {
			return -1
		}
	}
	for _, kw := range positiveReplyPhrases {
		if strings.Contains(t, kw) {
			return 1
		}
	}
	return 0
}

var negativeReplyPhrases = []string{
	"false positive", "not a bug", "not an issue", "not a real", "incorrect",
	"wrong", "noise", "wontfix", "won't fix", "disagree", "by design",
	"intended", "invalid", "misread", "hallucin",
}

var positiveReplyPhrases = []string{
	"fixed", "will fix", "fixing", "good catch", "nice catch", "great catch",
	"good find", "done", "resolved", "addressed", "updated", "agree",
	"thanks", "thank you", "you're right", "correct",
}

// missedBugSignal reports whether another reviewer's comment looks like a bug
// report that was acted on before merge. The bar is deliberately indirect —
// swatter can't judge "is this a bug" deterministically — so it requires an
// actioned signal (outdated anchor, resolved thread, or an affirming reply)
// and leaves "is it really a defect pattern" to the clustering pass.
func missedBugSignal(root ReviewCommentData, replies []ReviewCommentData, resolved map[int64]bool) bool {
	if len(strings.TrimSpace(root.Body)) < 12 {
		return false // "nit", "+1", emoji — not a bug report
	}
	if root.Outdated() || resolved[root.ID] {
		return true
	}
	for _, r := range replies {
		if _, own := parseFindingMarker(r.Body); own {
			continue
		}
		if classifyReply(r.Body) > 0 {
			return true
		}
	}
	return false
}

// oneLine flattens a comment/summary to a single trimmed line capped for the
// pending ledger (which must stay small — it rides in a committed file).
func oneLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	const max = 200
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
