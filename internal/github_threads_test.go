package internal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReviewThreads verifies the GraphQL response maps onto ReviewThread: the
// root comment is the first comment, participants union comment authors and
// reaction users (deduped), and resolution/id/path carry through. This is the
// parsing the live API can't be exercised for in CI.
func TestReviewThreads(t *testing.T) {
	const resp = `{"data":{"repository":{"pullRequest":{"reviewThreads":{
	  "pageInfo":{"hasNextPage":false,"endCursor":""},
	  "nodes":[
	    {"id":"THREAD_1","isResolved":false,"comments":{"nodes":[
	      {"author":{"login":"github-actions[bot]"},"body":"root body","path":"a.go",
	       "reactions":{"nodes":[{"user":{"login":"alice"}}]}},
	      {"author":{"login":"github-actions[bot]"},"body":"reply","path":"a.go",
	       "reactions":{"nodes":[]}}
	    ]}},
	    {"id":"THREAD_2","isResolved":true,"comments":{"nodes":[
	      {"author":{"login":"github-actions[bot]"},"body":"only bot","path":"b.go",
	       "reactions":{"nodes":[]}}
	    ]}}
	  ]
	}}}}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/graphql") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(resp))
	}))
	defer srv.Close()

	c := &GitHubClient{apiURL: srv.URL, owner: "o", repo: "r", token: "t", http: srv.Client()}
	got, err := c.ReviewThreads(context.Background(), 7)
	if err != nil {
		t.Fatalf("ReviewThreads: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 threads, got %d: %+v", len(got), got)
	}

	t1 := got[0]
	if t1.ThreadID != "THREAD_1" || t1.IsResolved || t1.RootAuthor != "github-actions[bot]" ||
		t1.RootBody != "root body" || t1.RootPath != "a.go" {
		t.Fatalf("thread 1 root fields wrong: %+v", t1)
	}
	// Participants: bot (root author, once) + alice (reactor). Deduped, bot appears once.
	if !hasLogin(t1.Participants, "github-actions[bot]") || !hasLogin(t1.Participants, "alice") {
		t.Fatalf("thread 1 participants missing expected logins: %v", t1.Participants)
	}
	if countLogin(t1.Participants, "github-actions[bot]") != 1 {
		t.Fatalf("bot login not deduped: %v", t1.Participants)
	}
	// This makes it human-engaged for reconcile.
	if !threadHumanEngaged(t1, "github-actions[bot]") {
		t.Fatalf("thread 1 should read as human-engaged (alice reacted)")
	}

	t2 := got[1]
	if t2.ThreadID != "THREAD_2" || !t2.IsResolved {
		t.Fatalf("thread 2 wrong: %+v", t2)
	}
	if threadHumanEngaged(t2, "github-actions[bot]") {
		t.Fatalf("thread 2 (bot only) must not be human-engaged: %v", t2.Participants)
	}
}

// TestResolveReviewThread_GraphQLError verifies a GraphQL-level error (returned
// with HTTP 200) is surfaced as an error, so the reporter treats a permission
// denial as a best-effort skip rather than a silent success.
func TestResolveReviewThread_GraphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// GitHub returns 200 with an errors array on an authorization failure.
		w.Write([]byte(`{"data":{"resolveReviewThread":null},"errors":[{"message":"Resource not accessible by integration"}]}`))
	}))
	defer srv.Close()

	c := &GitHubClient{apiURL: srv.URL, owner: "o", repo: "r", token: "t", http: srv.Client()}
	err := c.ResolveReviewThread(context.Background(), "THREAD_1")
	if err == nil {
		t.Fatal("want error surfaced from GraphQL errors field, got nil")
	}
	if !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("error should carry the GraphQL message, got: %v", err)
	}
}

// TestResolveReviewThread_Success verifies a clean mutation response is treated
// as success.
func TestResolveReviewThread_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":{"resolveReviewThread":{"thread":{"id":"THREAD_1"}}}}`))
	}))
	defer srv.Close()

	c := &GitHubClient{apiURL: srv.URL, owner: "o", repo: "r", token: "t", http: srv.Client()}
	if err := c.ResolveReviewThread(context.Background(), "THREAD_1"); err != nil {
		t.Fatalf("want success, got %v", err)
	}
}

func hasLogin(s []string, login string) bool {
	return countLogin(s, login) > 0
}

func countLogin(s []string, login string) int {
	n := 0
	for _, v := range s {
		if v == login {
			n++
		}
	}
	return n
}
