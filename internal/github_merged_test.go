package internal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ListMergedPRs must return only PRs merged in the window, skip closed-unmerged
// ones, carry each PR's base branch, and stop once updated_at (the sort key)
// falls before the window.
func TestListMergedPRs(t *testing.T) {
	now := time.Now().UTC()
	iso := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

	// Sorted by updated_at descending, as the API returns them:
	//   #50 merged 1h ago            → included (main)
	//   #49 closed, never merged     → skipped
	//   #48 merged 20h ago (release) → included (release)
	//   #39 updated 100h ago         → before the 72h window: halts the walk,
	//                                   and is itself excluded even though its
	//                                   merged_at would qualify.
	//   #38 merged 2h ago            → unreachable: the walk already stopped,
	//                                   proving the stop-on-updated short-circuit.
	page := fmt.Sprintf(`[
	  {"number":50,"merged_at":%q,"updated_at":%q,"base":{"ref":"main"}},
	  {"number":49,"merged_at":null,"updated_at":%q,"base":{"ref":"main"}},
	  {"number":48,"merged_at":%q,"updated_at":%q,"base":{"ref":"release"}},
	  {"number":39,"merged_at":%q,"updated_at":%q,"base":{"ref":"main"}},
	  {"number":38,"merged_at":%q,"updated_at":%q,"base":{"ref":"main"}}
	]`,
		iso(-1*time.Hour), iso(-1*time.Hour),
		iso(-2*time.Hour),
		iso(-20*time.Hour), iso(-20*time.Hour),
		iso(-80*time.Hour), iso(-100*time.Hour),
		iso(-2*time.Hour), iso(-2*time.Hour))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/pulls") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if q := r.URL.Query(); q.Get("state") != "closed" || q.Get("sort") != "updated" || q.Get("direction") != "desc" {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("page") == "1" {
			w.Write([]byte(page))
			return
		}
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := &GitHubClient{apiURL: srv.URL, owner: "o", repo: "r", token: "t", http: srv.Client()}
	got, err := c.ListMergedPRs(context.Background(), now.Add(-72*time.Hour))
	if err != nil {
		t.Fatalf("ListMergedPRs: %v", err)
	}

	wantRef := map[int]string{50: "main", 48: "release"}
	if len(got) != len(wantRef) {
		t.Fatalf("got %d merged PRs, want %d: %+v", len(got), len(wantRef), got)
	}
	for _, pr := range got {
		ref, ok := wantRef[pr.Number]
		if !ok {
			t.Fatalf("unexpected PR #%d in result (should be excluded)", pr.Number)
		}
		if pr.BaseRef != ref {
			t.Fatalf("PR #%d base = %q, want %q", pr.Number, pr.BaseRef, ref)
		}
	}
}

// A page shorter than per_page ends pagination; an empty window returns nothing
// without erroring.
func TestListMergedPRsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := &GitHubClient{apiURL: srv.URL, owner: "o", repo: "r", token: "t", http: srv.Client()}
	got, err := c.ListMergedPRs(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("ListMergedPRs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want no PRs, got %+v", got)
	}
}

// GetPR must hit /pulls/{n} and return the base ref, head sha, and title/body —
// the fields an issue_comment payload omits, which the on-demand @swatter review
// path fetches to build the packet and anchor the report.
func TestGetPR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/pulls/42" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`{
			"title": "Fix the widget",
			"body": "closes #7",
			"head": { "sha": "deadbeefcafe" },
			"base": { "ref": "main" }
		}`))
	}))
	defer srv.Close()

	c := &GitHubClient{apiURL: srv.URL, owner: "o", repo: "r", token: "t", http: srv.Client()}
	pr, err := c.GetPR(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if pr.BaseRef != "main" || pr.HeadSHA != "deadbeefcafe" {
		t.Errorf("refs = %q/%q, want main/deadbeefcafe", pr.BaseRef, pr.HeadSHA)
	}
	if pr.Title != "Fix the widget" || pr.Body != "closes #7" {
		t.Errorf("title/body = %q/%q", pr.Title, pr.Body)
	}
}
