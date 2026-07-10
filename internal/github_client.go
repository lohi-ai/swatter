package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// GitHubClient is a minimal REST client for the calls swatter needs: check
// runs, issue comments (the sticky summary), and PR reviews (inline comments).
// The harness owns the token — the review agents never see it — so an injected
// PR instruction can never drive a GitHub write.
type GitHubClient struct {
	token   string
	apiURL  string // e.g. https://api.github.com
	srvURL  string // e.g. https://github.com (for permalinks)
	owner   string
	repo    string
	http    *http.Client
}

// NewGitHubClientFromEnv builds a client from the Actions environment
// (GITHUB_TOKEN, GITHUB_API_URL, GITHUB_SERVER_URL, GITHUB_REPOSITORY). Returns
// (nil, nil) when no token is present — the caller then reports to stdout only.
func NewGitHubClientFromEnv() (*GitHubClient, error) {
	token := firstEnv("SWATTER_GITHUB_TOKEN", "GITHUB_TOKEN")
	if token == "" {
		return nil, nil
	}
	full := os.Getenv("GITHUB_REPOSITORY") // owner/repo
	owner, repo, ok := strings.Cut(full, "/")
	if !ok {
		return nil, fmt.Errorf("GITHUB_REPOSITORY %q is not owner/repo", full)
	}
	return &GitHubClient{
		token:  token,
		apiURL: envDefault("GITHUB_API_URL", "https://api.github.com"),
		srvURL: envDefault("GITHUB_SERVER_URL", "https://github.com"),
		owner:  owner,
		repo:   repo,
		http:   &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Permalink returns a browser URL to a file line at a commit sha.
func (c *GitHubClient) Permalink(sha, path string, line int) string {
	u := fmt.Sprintf("%s/%s/%s/blob/%s/%s", c.srvURL, c.owner, c.repo, sha, path)
	if line > 0 {
		u += fmt.Sprintf("#L%d", line)
	}
	return u
}

func (c *GitHubClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github %s %s: %d: %s", method, path, resp.StatusCode, truncate(string(data), 400))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// --- check runs ---

type checkRunReq struct {
	Name       string          `json:"name"`
	HeadSHA    string          `json:"head_sha"`
	Status     string          `json:"status"`               // queued|in_progress|completed
	Conclusion string          `json:"conclusion,omitempty"` // success|failure|neutral|...
	Output     *checkRunOutput `json:"output,omitempty"`
}

type checkRunOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// CreateCheckRun opens an in-progress check run and returns its id.
func (c *GitHubClient) CreateCheckRun(ctx context.Context, headSHA string) (int64, error) {
	var res struct {
		ID int64 `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/check-runs", c.owner, c.repo),
		checkRunReq{Name: "Swatter", HeadSHA: headSHA, Status: "in_progress"}, &res)
	return res.ID, err
}

// CompleteCheckRun sets the final conclusion + summary.
func (c *GitHubClient) CompleteCheckRun(ctx context.Context, id int64, conclusion, title, summary string) error {
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/check-runs/%d", c.owner, c.repo, id),
		checkRunReq{Name: "Swatter", Status: "completed", Conclusion: conclusion,
			Output: &checkRunOutput{Title: title, Summary: truncate(summary, 60_000)}}, nil)
}

// --- issue comments (sticky summary) ---

type issueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

// FindStickyComment returns the id of an existing swatter sticky comment on the
// PR (matched by the hidden marker), or 0 if none — the idempotency hook that
// makes re-pushes update in place instead of piling up comments.
func (c *GitHubClient) FindStickyComment(ctx context.Context, pr int, marker string) (int64, error) {
	var comments []issueComment
	err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100", c.owner, c.repo, pr), nil, &comments)
	if err != nil {
		return 0, err
	}
	for _, cm := range comments {
		if strings.Contains(cm.Body, marker) {
			return cm.ID, nil
		}
	}
	return 0, nil
}

// UpsertStickyComment creates or updates the sticky comment.
func (c *GitHubClient) UpsertStickyComment(ctx context.Context, pr int, id int64, body string) (int64, error) {
	if id != 0 {
		return id, c.do(ctx, http.MethodPatch,
			fmt.Sprintf("/repos/%s/%s/issues/comments/%d", c.owner, c.repo, id),
			issueComment{Body: body}, nil)
	}
	var res issueComment
	err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/repos/%s/%s/issues/%d/comments", c.owner, c.repo, pr),
		issueComment{Body: body}, &res)
	return res.ID, err
}

// --- reviews (inline comments) ---

type reviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"` // RIGHT
	Body string `json:"body"`
}

type reviewReq struct {
	CommitID string          `json:"commit_id"`
	Body     string          `json:"body"`
	Event    string          `json:"event"` // COMMENT (never APPROVE/REQUEST_CHANGES — Swatter reports, doesn't gate merges via review)
	Comments []reviewComment `json:"comments,omitempty"`
}

// CreateReview posts a single review carrying the inline comments. GitHub
// rejects the whole review if any comment targets a line not in the diff, so
// the caller must pre-filter with a DiffMap.
func (c *GitHubClient) CreateReview(ctx context.Context, pr int, commitID, body string, comments []reviewComment) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", c.owner, c.repo, pr),
		reviewReq{CommitID: commitID, Body: body, Event: "COMMENT", Comments: comments}, nil)
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
