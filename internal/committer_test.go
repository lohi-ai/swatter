package internal

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeContents simulates the Contents API with real compare-and-swap: a Put
// carrying a stale sha fails with ErrContentConflict, exactly like GitHub.
type fakeContents struct {
	content string
	version int // bumped on every accepted write; the sha is derived from it
	puts    int
	gets    int
	// raceOnce mutates the file between a Get and the next Put, once —
	// simulating a concurrent merge landing first.
	raceOnce func(current string) string
}

func (f *fakeContents) sha() string { return fmt.Sprintf("sha-%d", f.version) }

func (f *fakeContents) GetContent(_ context.Context, _, _ string) (string, string, bool, error) {
	f.gets++
	return f.content, f.sha(), f.content != "", nil
}

func (f *fakeContents) PutContent(_ context.Context, path, _, _, content, sha string) error {
	if race := f.raceOnce; race != nil {
		f.raceOnce = nil // clear first so a test may re-arm inside race
		f.content = race(f.content)
		f.version++
	}
	f.puts++
	if sha != f.sha() {
		return fmt.Errorf("%w: %s", ErrContentConflict, path)
	}
	f.content = content
	f.version++
	return nil
}

func TestCommitFileCASRetriesOnConflict(t *testing.T) {
	fake := &fakeContents{content: "base\n",
		raceOnce: func(cur string) string { return cur + "racer\n" }}

	changed, err := commitFileCAS(context.Background(), fake, "f.md", "main", "msg",
		func(current string) (string, error) { return current + "mine\n", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected a commit")
	}
	// The mutation was re-applied on top of the racer's content, not over it.
	if fake.content != "base\nracer\nmine\n" {
		t.Fatalf("content = %q — the concurrent write was clobbered", fake.content)
	}
	if fake.puts != 2 {
		t.Fatalf("puts = %d, want 2 (conflict then success)", fake.puts)
	}
}

func TestCommitFileCASSkipsUnchanged(t *testing.T) {
	fake := &fakeContents{content: "same\n"}
	changed, err := commitFileCAS(context.Background(), fake, "f.md", "main", "msg",
		func(current string) (string, error) { return current, nil }, nil)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v — an identical file must not stack commits", changed, err)
	}
	if fake.puts != 0 {
		t.Fatalf("puts = %d, want 0", fake.puts)
	}
}

func TestCommitFileCASGivesUp(t *testing.T) {
	fake := &fakeContents{content: "base\n"}
	// Every Put loses the race: re-arm raceOnce inside itself.
	var rearm func(string) string
	rearm = func(cur string) string { fake.raceOnce = rearm; return cur + "racer\n" }
	fake.raceOnce = rearm

	_, err := commitFileCAS(context.Background(), fake, "f.md", "main", "msg",
		func(current string) (string, error) { return current + "mine\n", nil }, nil)
	if err == nil || !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("err = %v, want give-up after %d conflicts", err, commitCASRetries)
	}
}

func TestCommitFileCASUsesSeedThenRefetches(t *testing.T) {
	// A seed lets the first attempt skip the GET. When the seed sha is fresh the
	// write lands with zero GETs; a stale seed just costs one refetch + retry.
	fake := &fakeContents{content: "base\n"}
	seed := &contentSeed{content: fake.content, sha: fake.sha()}
	changed, err := commitFileCAS(context.Background(), fake, "f.md", "main", "msg",
		func(current string) (string, error) { return current + "mine\n", nil }, seed)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if fake.gets != 0 {
		t.Fatalf("gets = %d, want 0 — the seed should have served the first attempt", fake.gets)
	}

	// Stale seed: a concurrent write bumped the sha before we tried, so the seeded
	// Put conflicts and the loop refetches and re-applies onto the fresh content.
	fake = &fakeContents{content: "base\n"}
	stale := &contentSeed{content: "base\n", sha: "sha-stale"}
	fake.raceOnce = func(cur string) string { return cur + "racer\n" }
	changed, err = commitFileCAS(context.Background(), fake, "f.md", "main", "msg",
		func(current string) (string, error) { return current + "mine\n", nil }, stale)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if fake.content != "base\nracer\nmine\n" {
		t.Fatalf("content = %q — stale seed clobbered the concurrent write", fake.content)
	}
	if fake.gets != 1 {
		t.Fatalf("gets = %d, want 1 (refetch after the seeded conflict)", fake.gets)
	}
}
