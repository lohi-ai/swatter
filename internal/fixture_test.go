package internal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPipelineFixture is the end-to-end replay: a vendored mini-repo whose head
// commit plants three real bugs (nil-deref, removed guard, SQL injection) plus
// one decoy that only looks suspicious. It asserts the pipeline finds ≥2 of the
// planted bugs and rejects the decoy. It calls a live model, so it is gated on
// SWATTER_LIVE_TEST + real BYOK env (SWATTER_API_KEY, SWATTER_MODEL, …). Unit
// coverage of the deterministic surface lives in the other _test.go files.
func TestPipelineFixture(t *testing.T) {
	if os.Getenv("SWATTER_LIVE_TEST") == "" {
		t.Skip("set SWATTER_LIVE_TEST=1 + SWATTER_API_KEY/SWATTER_MODEL to run the live fixture replay")
	}
	repo, base := buildFixtureRepo(t)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("config (need SWATTER_API_KEY, SWATTER_MODEL): %v", err)
	}
	cfg.RepoRoot = repo

	res, _, err := RunReview(context.Background(), RunOptions{
		Config:  cfg,
		BaseRef: base,
		HeadRef: "HEAD",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	planted := 0
	decoyFlagged := false
	for _, f := range res.Findings {
		switch {
		case containsAny(f.Summary+f.FailureScenario, "nil", "deref", "injection", "sql", "guard", "bounds", "permission"):
			planted++
		case containsAny(f.File, "decoy"):
			decoyFlagged = true
		}
	}
	if planted < 2 {
		t.Fatalf("want ≥2 planted bugs found, got %d (findings: %+v)", planted, res.Findings)
	}
	if decoyFlagged {
		t.Errorf("decoy was flagged — validator should have rejected it")
	}
	if RenderJSON(res) == "" {
		t.Error("findings JSON must be non-empty string")
	}
}

// buildFixtureRepo creates a throwaway git repo with a clean base commit and a
// head commit introducing the planted bugs. Returns the repo dir and the base
// commit sha.
func buildFixtureRepo(t *testing.T) (dir, baseSHA string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")

	write(t, dir, "store.go", baseStoreGo)
	run("add", ".")
	run("commit", "-qm", "base")
	baseSHA = trim(run("rev-parse", "HEAD"))

	write(t, dir, "store.go", buggyStoreGo)
	write(t, dir, "decoy.go", decoyGo)
	run("add", ".")
	run("commit", "-qm", "changes")
	return dir, baseSHA
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

func containsAny(hay string, needles ...string) bool {
	h := toLower(hay)
	for _, n := range needles {
		if indexOf(h, toLower(n)) >= 0 {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func indexOf(hay, needle string) int {
	if needle == "" {
		return 0
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

const baseStoreGo = `package fixture

import "database/sql"

type User struct{ Name string; Admin bool }

// getUser bounds-checks the index before indexing (the guard the head removes).
func getUser(users []*User, i int) *User {
	if i < 0 || i >= len(users) {
		return nil
	}
	return users[i]
}

// lookup uses a parameterized query (the head replaces it with concatenation).
func lookup(db *sql.DB, name string) (*sql.Rows, error) {
	return db.Query("SELECT * FROM users WHERE name = $1", name)
}
`

const buggyStoreGo = `package fixture

import (
	"database/sql"
	"fmt"
)

type User struct{ Name string; Admin bool }

// BUG 1 (removed guard): the bounds check is gone — getUser panics on i out of range.
func getUser(users []*User, i int) *User {
	return users[i]
}

// BUG 2 (nil deref): caller derefs the possibly-nil result without checking.
func userName(users []*User, i int) string {
	u := getUser(users, i)
	return u.Name
}

// BUG 3 (SQL injection): name is concatenated straight into the query.
func lookup(db *sql.DB, name string) (*sql.Rows, error) {
	q := fmt.Sprintf("SELECT * FROM users WHERE name = '%s'", name)
	return db.Query(q)
}
`

// decoyGo looks suspicious (a defer in a loop, an ignored error) but is
// actually fine — the loop is tiny and bounded, the error is genuinely
// ignorable. A good validator rejects a finding here.
const decoyGo = `package fixture

// sumThree is bounded to exactly three items; the defer-in-loop is harmless.
func sumThree(xs [3]int) int {
	total := 0
	for _, x := range xs {
		func() {
			defer func() {}()
			total += x
		}()
	}
	return total
}
`
