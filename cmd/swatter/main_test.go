package main

import (
	"errors"
	"testing"

	"github.com/lohi-ai/swatter/internal"
)

// learnBatch must process every PR even when one fails, default a missing base
// ref to main, count failures, and return non-zero only when at least one failed.
func TestLearnBatch(t *testing.T) {
	prs := []internal.MergedPR{
		{Number: 50, BaseRef: "main"},
		{Number: 48, BaseRef: "release"},
		{Number: 42, BaseRef: ""}, // missing base ref → defaulted to main
	}

	t.Run("one failure is skipped, run continues, returns 1", func(t *testing.T) {
		seen := map[int]string{}
		rc := learnBatch(prs, func(pr int, branch string) error {
			seen[pr] = branch
			if pr == 48 {
				return errors.New("boom")
			}
			return nil
		})
		if rc != 1 {
			t.Fatalf("return code = %d, want 1 (one PR failed)", rc)
		}
		// All three were attempted despite #48 erroring — no early exit.
		if len(seen) != 3 {
			t.Fatalf("processed %d PR(s), want all 3: %v", len(seen), seen)
		}
		if seen[42] != "main" {
			t.Fatalf("PR #42 branch = %q, want defaulted %q", seen[42], "main")
		}
		if seen[48] != "release" {
			t.Fatalf("PR #48 branch = %q, want %q", seen[48], "release")
		}
	})

	t.Run("all succeed returns 0", func(t *testing.T) {
		rc := learnBatch(prs, func(int, string) error { return nil })
		if rc != 0 {
			t.Fatalf("return code = %d, want 0", rc)
		}
	})

	t.Run("empty batch returns 0 without calling process", func(t *testing.T) {
		called := false
		rc := learnBatch(nil, func(int, string) error { called = true; return nil })
		if rc != 0 || called {
			t.Fatalf("empty batch: rc=%d called=%v, want 0/false", rc, called)
		}
	})
}
