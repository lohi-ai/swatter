package internal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func joinLines(l []string) string { return strings.Join(l, "\n") }

// TestPreflightRender_MissingResolve is the common case: a valid primary token
// and no PAT. The notice must clearly say resolve is not set and threads stay
// open, and reassure that the PAT (if set) is used for nothing else.
func TestPreflightRender_MissingResolve(t *testing.T) {
	out := joinLines(TokenPreflight{PrimaryOK: true}.Render())
	for _, want := range []string{
		"review agents never receive these tokens",
		"primary GITHUB_TOKEN",
		"resolve stale threads ONLY [not set]",
		"primary token ok (Actions/App installation token)",
		"resolve token MISSING",
		"pull-requests: write",
		"nothing but resolveReviewThread",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing-resolve notice lacks %q\n---\n%s", want, out)
		}
	}
}

// TestPreflightRender_ResolveOK shows the acting-as login and the single-purpose
// reassurance when a valid PAT is present.
func TestPreflightRender_ResolveOK(t *testing.T) {
	out := joinLines(TokenPreflight{
		PrimaryOK: true, PrimaryActor: "",
		ResolveSet: true, ResolveOK: true, ResolveActor: "octocat",
	}.Render())
	if !strings.Contains(out, "resolve token ok (acting as octocat)") {
		t.Errorf("want acting-as login, got:\n%s", out)
	}
	if strings.Contains(out, "[not set]") || strings.Contains(out, "MISSING") {
		t.Errorf("valid resolve token should not read as missing:\n%s", out)
	}
}

// TestPreflightRender_Failures surfaces both a dead primary token and an invalid
// PAT as explicit warnings rather than a silent ok.
func TestPreflightRender_Failures(t *testing.T) {
	out := joinLines(TokenPreflight{
		PrimaryErr: "github GET /repos/o/r: 401",
		ResolveSet: true, ResolveErr: "github GET /user: 401",
	}.Render())
	if !strings.Contains(out, "primary token FAILED repo access") {
		t.Errorf("want primary failure warning:\n%s", out)
	}
	if !strings.Contains(out, "resolve token INVALID") {
		t.Errorf("want resolve invalid warning:\n%s", out)
	}
}

// TestPreflightTokens_Probes verifies the client probes the right endpoints with
// the right token: the primary token reads the repo, the resolve PAT hits
// /user (validating it and naming its owner).
func TestPreflightTokens_Probes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		auth := req.Header.Get("Authorization")
		switch {
		case req.URL.Path == "/repos/o/r":
			if auth != "Bearer primary" {
				t.Errorf("repo probe should use primary token, got %q", auth)
			}
			w.Write([]byte(`{"full_name":"o/r"}`))
		case req.URL.Path == "/user":
			// The installation (primary) token is rejected at /user; the PAT works.
			if auth == "Bearer pat" {
				w.Write([]byte(`{"login":"octocat"}`))
				return
			}
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
		}
	}))
	defer srv.Close()

	c := &GitHubClient{apiURL: srv.URL, owner: "o", repo: "r", token: "primary", resolveToken: "pat", http: srv.Client()}
	p := c.PreflightTokens(context.Background())
	if !p.PrimaryOK {
		t.Fatalf("primary should be ok, got %+v", p)
	}
	if p.PrimaryActor != "" {
		t.Errorf("installation token has no /user actor, got %q", p.PrimaryActor)
	}
	if !p.ResolveSet || !p.ResolveOK || p.ResolveActor != "octocat" {
		t.Fatalf("resolve token should validate as octocat, got %+v", p)
	}
}

// TestPreflightTokens_NoResolve confirms that with no PAT configured the resolve
// branch stays unset and no probe is ever made with a resolve token. (The
// primary actor lookup may still hit /user with the primary token — that is the
// best-effort actor probe, not a resolve probe.)
func TestPreflightTokens_NoResolve(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/user" {
			// Only the primary token may reach here; a resolve token is not set.
			if auth := req.Header.Get("Authorization"); auth != "Bearer primary" {
				t.Errorf("/user probed with a non-primary token %q", auth)
			}
			w.WriteHeader(http.StatusForbidden) // installation token → no actor
			return
		}
		w.Write([]byte(`{"full_name":"o/r"}`))
	}))
	defer srv.Close()

	c := &GitHubClient{apiURL: srv.URL, owner: "o", repo: "r", token: "primary", http: srv.Client()}
	p := c.PreflightTokens(context.Background())
	if !p.PrimaryOK || p.ResolveSet || p.ResolveOK {
		t.Fatalf("want primary ok and resolve unset, got %+v", p)
	}
}
