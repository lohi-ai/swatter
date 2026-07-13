package internal

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestObsLedgerRoundTrip(t *testing.T) {
	l := &ObsLedger{}
	l.Add(Observation{Kind: ObsMissed, PR: 42, Date: "2026-07-11", Path: "db/tx.go",
		Note: "advisory lock never released   on error: path\nsecond line dropped"})
	l.Add(Observation{Kind: ObsRepeat, PR: 43, Date: "2026-07-11", Note: "SQL built by concat"})

	got := ParseObsLedger(l.Render())
	if len(got.Obs) != 2 {
		t.Fatalf("round-trip lost entries: %+v", got.Obs)
	}
	o := got.Obs[0]
	if o.Kind != ObsMissed || o.PR != 42 || o.Date != "2026-07-11" || o.Path != "db/tx.go" {
		t.Fatalf("fields mangled: %+v", o)
	}
	// note survives colons, collapses runs of spaces (the 3-space field
	// separator), and keeps only the first line.
	if o.Note != "advisory lock never released on error: path" {
		t.Fatalf("note = %q", o.Note)
	}
	if o.ID != "o-2026-07-11-1" || got.Obs[1].ID != "o-2026-07-11-2" {
		t.Fatalf("ids = %q, %q", o.ID, got.Obs[1].ID)
	}
}

func TestObsLedgerAddDedupsSamePR(t *testing.T) {
	l := &ObsLedger{}
	if !l.Add(Observation{Kind: ObsRepeat, PR: 1, Date: "2026-07-11", Note: "SQL built by concat"}) {
		t.Fatal("first add rejected")
	}
	// Re-running the learn flow on the same PR must not double-record.
	if l.Add(Observation{Kind: ObsRepeat, PR: 1, Date: "2026-07-11", Note: "SQL built by concat!"}) {
		t.Fatal("same PR + note re-added")
	}
	// The same pattern on a different PR is the accumulating evidence we want.
	if !l.Add(Observation{Kind: ObsRepeat, PR: 2, Date: "2026-07-12", Note: "SQL built by concat"}) {
		t.Fatal("different PR rejected")
	}
}

func TestObsLedgerPrune(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	l := &ObsLedger{}
	l.Add(Observation{Kind: ObsRepeat, PR: 1, Date: "2026-01-01", Note: "ancient"}) // >120d
	l.Add(Observation{Kind: ObsRepeat, PR: 2, Date: "2026-07-01", Note: "fresh"})
	l.Prune(now)
	if len(l.Obs) != 1 || l.Obs[0].Note != "fresh" {
		t.Fatalf("age prune wrong: %+v", l.Obs)
	}

	l = &ObsLedger{}
	for i := 0; i < obsMaxEntries+10; i++ {
		l.Add(Observation{Kind: ObsRepeat, PR: i, Date: fmt.Sprintf("2026-07-%02d", i%28+1),
			Note: fmt.Sprintf("pattern %d", i)})
	}
	l.Prune(now)
	if len(l.Obs) != obsMaxEntries {
		t.Fatalf("cap prune: %d entries, want %d", len(l.Obs), obsMaxEntries)
	}
}

func TestClusterEvidence(t *testing.T) {
	l := &ObsLedger{}
	l.Add(Observation{Kind: ObsMissed, PR: 1, Date: "2026-07-11", Note: "lock leak on error"})
	l.Add(Observation{Kind: ObsRepeat, PR: 2, Date: "2026-07-11", Note: "lock leak in retry"})
	l.Add(Observation{Kind: ObsRepeat, PR: 2, Date: "2026-07-11", Note: "lock leak in shutdown"})

	ids := []string{l.Obs[0].ID, l.Obs[1].ID, l.Obs[2].ID, "o-bogus-99"}
	weight, prs, valid := l.ClusterEvidence(ids)
	if weight != 4 { // missed(2) + repeat(1) + repeat(1); bogus id ignored
		t.Fatalf("weight = %d, want 4", weight)
	}
	if prs != 2 {
		t.Fatalf("distinct PRs = %d, want 2", prs)
	}
	if len(valid) != 3 {
		t.Fatalf("valid = %v", valid)
	}

	l.Remove(valid)
	if len(l.Obs) != 0 {
		t.Fatalf("Remove left %+v", l.Obs)
	}
}

func TestRemoveIdentitiesSurvivesIDReassignment(t *testing.T) {
	// Promotion planning spends PR 1's observation (id o-2026-07-11-1). On the
	// CAS refetch, a concurrent run has already landed a DIFFERENT observation
	// that now occupies that same generated id. Removing by id would delete the
	// concurrent entry; removing by (PR, note) identity deletes only PR 1's.
	planned := Observation{Kind: ObsMissed, PR: 1, Date: "2026-07-11", Note: "lock leak on error"}
	plan := &ObsLedger{}
	plan.Add(planned)
	spent := spentIdentityOf(planned)
	if plan.Obs[0].ID != "o-2026-07-11-1" {
		t.Fatalf("precondition: planned id = %q", plan.Obs[0].ID)
	}

	current := &ObsLedger{}
	current.Add(Observation{Kind: ObsRepeat, PR: 2, Date: "2026-07-11", Note: "unrelated concurrent finding"})
	current.Add(planned) // this run re-adds its own; here it gets o-...-2
	if current.Obs[0].ID != "o-2026-07-11-1" {
		t.Fatalf("precondition: concurrent entry should hold the colliding id, got %q", current.Obs[0].ID)
	}

	current.RemoveIdentities([]obsIdentity{spent})
	if len(current.Obs) != 1 || current.Obs[0].PR != 2 {
		t.Fatalf("identity removal deleted the wrong observation: %+v", current.Obs)
	}
}

func spentIdentityOf(o Observation) obsIdentity {
	o.Note = collapseSpaces(oneLine(o.Note))
	return o.identity()
}

// PromotionPossible is the necessary-condition gate in front of the clustering
// LLM call: enough total weight AND at least two distinct PRs.
func TestObsLedgerPromotionPossible(t *testing.T) {
	l := &ObsLedger{}
	if l.PromotionPossible(3) {
		t.Fatal("empty ledger cannot promote")
	}
	l.Add(Observation{Kind: ObsMissed, PR: 1, Date: "2026-07-14", Note: "missed nil check on decode"}) // weight 2
	l.Add(Observation{Kind: ObsRepeat, PR: 1, Date: "2026-07-14", Note: "repeat: unchecked error"})    // weight 1
	if l.PromotionPossible(3) {
		t.Fatal("weight 3 from a single PR must not pass (needs ≥ 2 distinct PRs)")
	}
	l.Add(Observation{Kind: ObsRepeat, PR: 2, Date: "2026-07-14", Note: "another unchecked error"}) // weight 1, second PR
	if !l.PromotionPossible(3) {
		t.Fatal("weight 4 across 2 PRs must pass")
	}
	if l.PromotionPossible(5) {
		t.Fatal("threshold above total weight must not pass")
	}
}

func TestObsLedgerEmptyRender(t *testing.T) {
	l := ParseObsLedger("")
	if len(l.Obs) != 0 {
		t.Fatalf("empty parse: %+v", l.Obs)
	}
	if !strings.Contains(l.Render(), "No pending observations") {
		t.Fatalf("empty render: %q", l.Render())
	}
}
