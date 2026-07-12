package internal

import "strings"

// reconcile compares this round's findings against Swatter's existing inline
// comment threads on the PR and splits the work for a re-review:
//
//   - toPost: findings with no open Swatter thread yet — post them (new). A
//     finding that already has an open thread is dropped (persistent): it keeps
//     its existing comment instead of getting a duplicate every round.
//   - toResolve: open Swatter threads whose finding is gone from this round
//     (stale) — resolve them, UNLESS a human engaged with the thread, in which
//     case it's left open so Swatter never closes a person's conversation.
//
// A thread counts as Swatter's only when its root comment carries the finding
// marker AND was authored by swatterLogin — the marker is copyable text, so
// authorship is required (mirrors AnalyzeFeedback). Match key is (path,
// summary): robust to line drift across commits, and a reworded finding is
// treated as new (old thread resolves, new comment posts). Resolved threads are
// ignored entirely — never re-resolved, never blocking a re-post.
//
// Pure and deterministic: toPost preserves the input finding order, toResolve
// follows thread order.
func reconcile(current []Finding, threads []ReviewThread, swatterLogin string) (toPost []Finding, toResolve []string) {
	type key struct{ path, summary string }

	// Without a known Swatter login we can't safely claim any thread as ours,
	// so fall back to the pre-reconcile behavior: post everything, resolve none.
	if swatterLogin == "" {
		return append([]Finding(nil), current...), nil
	}

	swatterThread := func(t ReviewThread) (findingMarker, bool) {
		if t.IsResolved {
			return findingMarker{}, false
		}
		m, ok := parseFindingMarker(t.RootBody)
		if !ok || !sameLogin(t.RootAuthor, swatterLogin) {
			return findingMarker{}, false
		}
		return m, true
	}

	// Keys of open Swatter threads currently on the PR.
	openKeys := map[key]bool{}
	for _, t := range threads {
		if m, ok := swatterThread(t); ok {
			openKeys[key{t.RootPath, m.Summary}] = true
		}
	}

	// New findings post; persistent ones (open thread exists) are dropped.
	currentKeys := map[key]bool{}
	for _, f := range current {
		k := key{f.File, f.Summary}
		currentKeys[k] = true
		if !openKeys[k] {
			toPost = append(toPost, f)
		}
	}

	// Open Swatter threads whose finding is gone this round → resolve, unless a
	// human engaged with the thread.
	for _, t := range threads {
		m, ok := swatterThread(t)
		if !ok {
			continue
		}
		if currentKeys[key{t.RootPath, m.Summary}] {
			continue // still reported — leave open
		}
		if threadHumanEngaged(t, swatterLogin) {
			continue // a person is in the thread — don't auto-close it
		}
		toResolve = append(toResolve, t.ThreadID)
	}
	return toPost, toResolve
}

// threadHumanEngaged reports whether anyone other than Swatter commented on or
// reacted to the thread.
func threadHumanEngaged(t ReviewThread, swatterLogin string) bool {
	for _, p := range t.Participants {
		if p != "" && !sameLogin(p, swatterLogin) {
			return true
		}
	}
	return false
}

// sameLogin compares two GitHub actor logins, tolerating the "[bot]" suffix
// discrepancy between GitHub's APIs: the REST API reports a bot as
// "github-actions[bot]" (the form BotLogin defaults to), while GraphQL — the
// source of review-thread authors and participants — reports the same actor as
// "github-actions", with no suffix. Without trimming it, Swatter never
// recognizes its own GraphQL-sourced threads: the trust check fails (nothing
// deduped or resolved) and its own participation reads as a human's (threads
// left open). Comparison is case-insensitive, matching the call sites.
func sameLogin(a, b string) bool {
	return strings.EqualFold(trimBotSuffix(a), trimBotSuffix(b))
}

func trimBotSuffix(login string) string {
	if len(login) >= 5 && strings.EqualFold(login[len(login)-5:], "[bot]") {
		return login[:len(login)-5]
	}
	return login
}
