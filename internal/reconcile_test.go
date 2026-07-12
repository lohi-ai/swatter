package internal

import (
	"reflect"
	"testing"
)

// swatterThreadOf builds a review thread whose root comment is a genuine Swatter
// inline finding (marker + bot author), for the reconcile tests. participants are
// extra logins (repliers/reactors) beyond Swatter itself.
func swatterThreadOf(id, path, summary string, resolved bool, participants ...string) ReviewThread {
	f := Finding{Candidate: Candidate{File: path, Line: 3, Summary: summary, Severity: SevMajor}, Verdict: VerdictConfirmed}
	return ReviewThread{
		ThreadID:     id,
		IsResolved:   resolved,
		RootAuthor:   testBotLogin,
		RootBody:     renderInline(f),
		RootPath:     path,
		Participants: append([]string{testBotLogin}, participants...),
	}
}

func findingOf(path, summary string) Finding {
	return Finding{Candidate: Candidate{File: path, Line: 3, Summary: summary, Severity: SevMajor}, Verdict: VerdictConfirmed}
}

func summaries(fs []Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Summary
	}
	return out
}

// TestReconcile_GraphQLBotLoginNoSuffix is the regression test for the live bug
// the httptest simulation missed: GitHub's GraphQL API reports the bot author as
// "github-actions" (no "[bot]"), while BotLogin defaults to the REST form
// "github-actions[bot]". The thread must still be recognized as Swatter's —
// otherwise nothing is deduped or resolved. A stale thread here must resolve,
// and Swatter's own no-suffix participation must NOT read as human engagement.
func TestReconcile_GraphQLBotLoginNoSuffix(t *testing.T) {
	const (
		graphQLBot = "github-actions"      // as reviewThreads(){author{login}} returns it
		restBot    = "github-actions[bot]" // as cfg.BotLogin defaults
	)
	f := findingOf("a.go", "nil deref on x")
	th := swatterThreadOf("T1", "a.go", "nil deref on x", false)
	th.RootAuthor = graphQLBot             // GraphQL form on the thread
	th.Participants = []string{graphQLBot} // only the bot participated

	// Persistent finding: recognized as Swatter's despite the suffix mismatch → deduped.
	post, resolve := reconcile([]Finding{f}, []ReviewThread{th}, restBot)
	if len(post) != 0 {
		t.Fatalf("persistent finding should dedup across the [bot]-suffix gap, got %v", summaries(post))
	}
	if len(resolve) != 0 {
		t.Fatalf("persistent thread should not resolve, got %v", resolve)
	}

	// Stale finding (gone this round): the bot-only thread must resolve, not be
	// mistaken for human-engaged via its no-suffix participant.
	_, resolve = reconcile(nil, []ReviewThread{th}, restBot)
	if !reflect.DeepEqual(resolve, []string{"T1"}) {
		t.Fatalf("stale bot-only thread should resolve across the [bot]-suffix gap, got %v", resolve)
	}
}

func TestReconcile_NewFindingPosts(t *testing.T) {
	f := findingOf("a.go", "nil deref on x")
	post, resolve := reconcile([]Finding{f}, nil, testBotLogin)
	if got := summaries(post); !reflect.DeepEqual(got, []string{"nil deref on x"}) {
		t.Fatalf("want new finding posted, got %v", got)
	}
	if len(resolve) != 0 {
		t.Fatalf("want no resolves, got %v", resolve)
	}
}

func TestReconcile_PersistentSkipped(t *testing.T) {
	f := findingOf("a.go", "nil deref on x")
	th := swatterThreadOf("T1", "a.go", "nil deref on x", false)
	post, resolve := reconcile([]Finding{f}, []ReviewThread{th}, testBotLogin)
	if len(post) != 0 {
		t.Fatalf("persistent finding should not be re-posted, got %v", summaries(post))
	}
	if len(resolve) != 0 {
		t.Fatalf("persistent thread should not resolve, got %v", resolve)
	}
}

func TestReconcile_StaleSwatterOnlyResolves(t *testing.T) {
	th := swatterThreadOf("T1", "a.go", "nil deref on x", false)
	post, resolve := reconcile(nil, []ReviewThread{th}, testBotLogin)
	if len(post) != 0 {
		t.Fatalf("no current findings, nothing to post, got %v", summaries(post))
	}
	if !reflect.DeepEqual(resolve, []string{"T1"}) {
		t.Fatalf("stale swatter-only thread should resolve, got %v", resolve)
	}
}

func TestReconcile_StaleHumanEngagedLeftOpen(t *testing.T) {
	// Same stale thread, but a human replied/reacted → leave it open.
	th := swatterThreadOf("T1", "a.go", "nil deref on x", false, "alice")
	_, resolve := reconcile(nil, []ReviewThread{th}, testBotLogin)
	if len(resolve) != 0 {
		t.Fatalf("human-engaged stale thread must stay open, got %v", resolve)
	}
}

func TestReconcile_RewordedIsNew(t *testing.T) {
	// Old thread's summary no longer matches the (reworded) current finding:
	// the old thread resolves and the new wording posts.
	old := swatterThreadOf("T1", "a.go", "nil deref on x", false)
	cur := findingOf("a.go", "possible nil pointer dereference on x")
	post, resolve := reconcile([]Finding{cur}, []ReviewThread{old}, testBotLogin)
	if got := summaries(post); !reflect.DeepEqual(got, []string{"possible nil pointer dereference on x"}) {
		t.Fatalf("reworded finding should post as new, got %v", got)
	}
	if !reflect.DeepEqual(resolve, []string{"T1"}) {
		t.Fatalf("old thread should resolve, got %v", resolve)
	}
}

func TestReconcile_NonSwatterAuthorIgnored(t *testing.T) {
	// A thread carrying a (copied) marker but authored by someone else is not
	// Swatter's: it never resolves, and a current finding at that location still
	// posts (it isn't deduped against a foreign thread).
	th := swatterThreadOf("T1", "a.go", "nil deref on x", false)
	th.RootAuthor = "alice"
	cur := findingOf("a.go", "nil deref on x")
	post, resolve := reconcile([]Finding{cur}, []ReviewThread{th}, testBotLogin)
	if got := summaries(post); !reflect.DeepEqual(got, []string{"nil deref on x"}) {
		t.Fatalf("finding should post (foreign thread doesn't dedup), got %v", got)
	}
	if len(resolve) != 0 {
		t.Fatalf("foreign thread must not resolve, got %v", resolve)
	}
}

func TestReconcile_AlreadyResolvedNotTouched(t *testing.T) {
	// A resolved thread neither blocks a re-post of its finding nor gets resolved
	// again (reappearing finding is treated as new).
	th := swatterThreadOf("T1", "a.go", "nil deref on x", true)
	cur := findingOf("a.go", "nil deref on x")
	post, resolve := reconcile([]Finding{cur}, []ReviewThread{th}, testBotLogin)
	if got := summaries(post); !reflect.DeepEqual(got, []string{"nil deref on x"}) {
		t.Fatalf("finding should re-post over a resolved thread, got %v", got)
	}
	if len(resolve) != 0 {
		t.Fatalf("resolved thread must not resolve again, got %v", resolve)
	}
}

func TestReconcile_EmptyLoginFallsBack(t *testing.T) {
	// With no known Swatter login we can't claim any thread → post everything,
	// resolve nothing.
	th := swatterThreadOf("T1", "a.go", "nil deref on x", false)
	cur := findingOf("a.go", "nil deref on x")
	post, resolve := reconcile([]Finding{cur}, []ReviewThread{th}, "")
	if len(post) != 1 {
		t.Fatalf("empty login should post everything, got %v", summaries(post))
	}
	if len(resolve) != 0 {
		t.Fatalf("empty login should resolve nothing, got %v", resolve)
	}
}

func TestReconcile_MixedRound(t *testing.T) {
	// One PR, one round: a new finding, a persistent one, a stale swatter-only
	// thread, and a stale human-engaged thread — all handled together.
	threads := []ReviewThread{
		swatterThreadOf("T-persist", "a.go", "persistent bug", false),
		swatterThreadOf("T-stale", "b.go", "fixed bug", false),
		swatterThreadOf("T-human", "c.go", "fixed but discussed", false, "alice"),
	}
	current := []Finding{
		findingOf("a.go", "persistent bug"), // matches T-persist → skip
		findingOf("d.go", "brand new bug"),  // no thread → post
	}
	post, resolve := reconcile(current, threads, testBotLogin)
	if got := summaries(post); !reflect.DeepEqual(got, []string{"brand new bug"}) {
		t.Fatalf("only the new finding should post, got %v", got)
	}
	if !reflect.DeepEqual(resolve, []string{"T-stale"}) {
		t.Fatalf("only the stale swatter-only thread should resolve, got %v", resolve)
	}
}
