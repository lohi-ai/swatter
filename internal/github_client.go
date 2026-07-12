package internal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// GitHubClient is a minimal REST client for the calls swatter needs: check
// runs, issue comments (the sticky summary), and PR reviews (inline comments).
// The harness owns the token — the review agents never see it — so an injected
// PR instruction can never drive a GitHub write.
type GitHubClient struct {
	token  string
	apiURL string // e.g. https://api.github.com
	srvURL string // e.g. https://github.com (for permalinks)
	owner  string
	repo   string
	http   *http.Client

	// resolveToken authenticates the resolveReviewThread mutation only. The
	// default Actions GITHUB_TOKEN cannot resolve threads ("Resource not
	// accessible by integration") even with pull-requests:write, so a PAT (or
	// App token) with that right is supplied separately via SWATTER_RESOLVE_TOKEN.
	// Empty means stale-thread resolution is skipped (dedup still works).
	resolveToken string
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
		token:        token,
		apiURL:       envDefault("GITHUB_API_URL", "https://api.github.com"),
		srvURL:       envDefault("GITHUB_SERVER_URL", "https://github.com"),
		owner:        owner,
		repo:         repo,
		http:         &http.Client{Timeout: 30 * time.Second},
		resolveToken: strings.TrimSpace(os.Getenv("SWATTER_RESOLVE_TOKEN")),
	}, nil
}

// CanResolveThreads reports whether a resolve-capable token is configured. When
// false the reporter skips the resolveReviewThread loop entirely rather than
// firing calls the default GITHUB_TOKEN is known to reject.
func (c *GitHubClient) CanResolveThreads() bool { return c.resolveToken != "" }

// Permalink returns a browser URL to a file line at a commit sha.
func (c *GitHubClient) Permalink(sha, path string, line int) string {
	u := fmt.Sprintf("%s/%s/%s/blob/%s/%s", c.srvURL, c.owner, c.repo, sha, path)
	if line > 0 {
		u += fmt.Sprintf("#L%d", line)
	}
	return u
}

func (c *GitHubClient) do(ctx context.Context, method, path string, body any, out any) error {
	_, err := c.doStatus(ctx, method, path, body, out)
	return err
}

// doStatus is do plus the HTTP status code, for callers that must distinguish
// specific failures (404 = absent file, 409/422 = contents sha conflict). It
// authenticates as the primary token; doStatusAs authenticates as a caller-
// chosen token (the resolve PAT).
func (c *GitHubClient) doStatus(ctx context.Context, method, path string, body any, out any) (int, error) {
	return c.doStatusAs(ctx, c.token, method, path, body, out)
}

func (c *GitHubClient) doStatusAs(ctx context.Context, authToken, method, path string, body any, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiURL+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("github %s %s: %d: %s", method, path, resp.StatusCode, truncate(string(data), 400))
	}
	if out != nil && len(data) > 0 {
		return resp.StatusCode, json.Unmarshal(data, out)
	}
	return resp.StatusCode, nil
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

// --- post-merge feedback read-back ---

// ReviewCommentData is the subset of a pull-request review comment the
// feedback pass needs: identity, threading, anchor, reactions, and whether the
// commented line was changed by a later commit (Position == nil → "outdated",
// GitHub's signal that the flagged code moved or was rewritten before merge).
type ReviewCommentData struct {
	ID          int64  `json:"id"`
	InReplyToID int64  `json:"in_reply_to_id"`
	Body        string `json:"body"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Position    *int   `json:"position"`
	User        struct {
		Login string `json:"login"`
		Type  string `json:"type"` // "User" | "Bot"
	} `json:"user"`
	Reactions struct {
		Up   int `json:"+1"`
		Down int `json:"-1"`
	} `json:"reactions"`
}

// Outdated reports whether the commented line was changed by a later commit.
func (rc ReviewCommentData) Outdated() bool { return rc.Position == nil }

// ListReviewComments fetches every inline review comment on a PR, following
// pagination to the end so feedback on a very large PR is never truncated.
func (c *GitHubClient) ListReviewComments(ctx context.Context, pr int) ([]ReviewCommentData, error) {
	var all []ReviewCommentData
	for page := 1; ; page++ {
		var batch []ReviewCommentData
		err := c.do(ctx, http.MethodGet,
			fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=100&page=%d", c.owner, c.repo, pr, page), nil, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break // last (short) page — a full page means there may be more
		}
	}
	return all, nil
}

// ThreadResolution returns, per review-comment id, whether its review thread
// was resolved. Thread resolution is not exposed over REST, so this is the one
// GraphQL call swatter makes. It follows the reviewThreads cursor to the end so
// resolutions on a PR with many threads aren't silently dropped. Best-effort:
// callers treat an error as "no resolution data" rather than failing the pass.
func (c *GitHubClient) ThreadResolution(ctx context.Context, pr int) (map[int64]bool, error) {
	const q = `query($owner:String!,$repo:String!,$pr:Int!,$cursor:String){
  repository(owner:$owner,name:$repo){ pullRequest(number:$pr){
    reviewThreads(first:100, after:$cursor){
      pageInfo{ hasNextPage endCursor }
      nodes{ isResolved comments(first:50){ nodes{ databaseId } } }
    }
  } } }`
	out := map[int64]bool{}
	var cursor *string
	for {
		body := map[string]any{
			"query":     q,
			"variables": map[string]any{"owner": c.owner, "repo": c.repo, "pr": pr, "cursor": cursor},
		}
		var res struct {
			Data struct {
				Repository struct {
					PullRequest struct {
						ReviewThreads struct {
							PageInfo struct {
								HasNextPage bool   `json:"hasNextPage"`
								EndCursor   string `json:"endCursor"`
							} `json:"pageInfo"`
							Nodes []struct {
								IsResolved bool `json:"isResolved"`
								Comments   struct {
									Nodes []struct {
										DatabaseID int64 `json:"databaseId"`
									} `json:"nodes"`
								} `json:"comments"`
							} `json:"nodes"`
						} `json:"reviewThreads"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := c.do(ctx, http.MethodPost, "/graphql", body, &res); err != nil {
			return nil, err
		}
		threads := res.Data.Repository.PullRequest.ReviewThreads
		for _, th := range threads.Nodes {
			for _, cm := range th.Comments.Nodes {
				out[cm.DatabaseID] = th.IsResolved
			}
		}
		if !threads.PageInfo.HasNextPage || threads.PageInfo.EndCursor == "" {
			break
		}
		end := threads.PageInfo.EndCursor
		cursor = &end
	}
	return out, nil
}

// --- review-thread reconcile (multi-round dedup + stale resolve) ---

// ReviewThread is one PR review-comment thread as the reconciler needs it: the
// GraphQL node id (the resolve mutation keys on it, not a comment databaseId),
// resolution state, the root comment's author/body/path (identity + finding
// marker), and the deduped logins of everyone who commented or reacted anywhere
// in the thread, so a Swatter-only thread can be told from a human-engaged one.
type ReviewThread struct {
	ThreadID     string
	IsResolved   bool
	RootAuthor   string
	RootBody     string
	RootPath     string
	Participants []string // every comment author + reaction user, deduped
}

// ReviewThreads fetches every review-comment thread on a PR with the metadata
// the reconciler needs. It walks the reviewThreads cursor to the end (like
// ThreadResolution) so threads aren't dropped on a large PR; per-thread comments
// and reactions are capped, which only understates human-engagement on a
// pathologically large thread. Best-effort: the caller falls back to posting
// every finding when this errors.
func (c *GitHubClient) ReviewThreads(ctx context.Context, pr int) ([]ReviewThread, error) {
	const q = `query($owner:String!,$repo:String!,$pr:Int!,$cursor:String){
  repository(owner:$owner,name:$repo){ pullRequest(number:$pr){
    reviewThreads(first:100, after:$cursor){
      pageInfo{ hasNextPage endCursor }
      nodes{
        id
        isResolved
        comments(first:100){ nodes{
          author{ login }
          body
          path
          reactions(first:20){ nodes{ user{ login } } }
        } }
      }
    }
  } } }`
	var out []ReviewThread
	var cursor *string
	for {
		body := map[string]any{
			"query":     q,
			"variables": map[string]any{"owner": c.owner, "repo": c.repo, "pr": pr, "cursor": cursor},
		}
		var res struct {
			Data struct {
				Repository struct {
					PullRequest struct {
						ReviewThreads struct {
							PageInfo struct {
								HasNextPage bool   `json:"hasNextPage"`
								EndCursor   string `json:"endCursor"`
							} `json:"pageInfo"`
							Nodes []struct {
								ID         string `json:"id"`
								IsResolved bool   `json:"isResolved"`
								Comments   struct {
									Nodes []struct {
										Author struct {
											Login string `json:"login"`
										} `json:"author"`
										Body      string `json:"body"`
										Path      string `json:"path"`
										Reactions struct {
											Nodes []struct {
												User struct {
													Login string `json:"login"`
												} `json:"user"`
											} `json:"nodes"`
										} `json:"reactions"`
									} `json:"nodes"`
								} `json:"comments"`
							} `json:"nodes"`
						} `json:"reviewThreads"`
					} `json:"pullRequest"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := c.do(ctx, http.MethodPost, "/graphql", body, &res); err != nil {
			return nil, err
		}
		for _, th := range res.Data.Repository.PullRequest.ReviewThreads.Nodes {
			t := ReviewThread{ThreadID: th.ID, IsResolved: th.IsResolved}
			seen := map[string]bool{}
			addParticipant := func(login string) {
				if login == "" || seen[login] {
					return
				}
				seen[login] = true
				t.Participants = append(t.Participants, login)
			}
			for i, cm := range th.Comments.Nodes {
				if i == 0 {
					t.RootAuthor, t.RootBody, t.RootPath = cm.Author.Login, cm.Body, cm.Path
				}
				addParticipant(cm.Author.Login)
				for _, rx := range cm.Reactions.Nodes {
					addParticipant(rx.User.Login)
				}
			}
			out = append(out, t)
		}
		page := res.Data.Repository.PullRequest.ReviewThreads.PageInfo
		if !page.HasNextPage || page.EndCursor == "" {
			break
		}
		end := page.EndCursor
		cursor = &end
	}
	return out, nil
}

// ResolveReviewThread marks a review thread resolved via GraphQL (no REST
// equivalent). It authenticates with resolveToken, not the primary token: the
// default Actions GITHUB_TOKEN is rejected here even with pull-requests:write.
// GraphQL reports authorization/argument failures in the response body with a
// 200 status, so a bare do would treat them as success — parse the errors field
// so a permission denial surfaces to the caller as an error (the reporter treats
// it as a best-effort skip, never a failed check run).
func (c *GitHubClient) ResolveReviewThread(ctx context.Context, threadID string) error {
	tok := c.resolveToken
	if tok == "" {
		tok = c.token // defensive: callers gate on CanResolveThreads first
	}
	const m = `mutation($threadId:ID!){ resolveReviewThread(input:{threadId:$threadId}){ thread{ id } } }`
	body := map[string]any{"query": m, "variables": map[string]any{"threadId": threadID}}
	var res struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := c.doStatusAs(ctx, tok, http.MethodPost, "/graphql", body, &res); err != nil {
		return err
	}
	if len(res.Errors) > 0 {
		return fmt.Errorf("resolveReviewThread %s: %s", threadID, res.Errors[0].Message)
	}
	return nil
}

// --- merged-PR enumeration (the scheduled learn batch) ---

// MergedPR is the slice of a closed pull request the batch learn flow needs:
// its number and the base branch its rule-book updates commit to.
type MergedPR struct {
	Number   int
	BaseRef  string
	MergedAt time.Time
}

// ListMergedPRs returns every PR merged at or after since, newest-merge first.
// It walks the closed-PR list sorted by updated_at descending: a merged PR's
// updated_at is always ≥ its merged_at, so once a page yields a PR updated
// before the window every remaining PR is older too and we stop. A closed-but-
// unmerged PR (merged_at == null) is skipped. The window is meant to overlap
// prior runs — RunFeedback is idempotent per PR (RuleStore.HasScored), so a PR
// seen twice is folded in once, which makes a missed nightly run self-heal.
func (c *GitHubClient) ListMergedPRs(ctx context.Context, since time.Time) ([]MergedPR, error) {
	var out []MergedPR
	for page := 1; ; page++ {
		var batch []struct {
			Number    int     `json:"number"`
			MergedAt  *string `json:"merged_at"`
			UpdatedAt string  `json:"updated_at"`
			Base      struct {
				Ref string `json:"ref"`
			} `json:"base"`
		}
		err := c.do(ctx, http.MethodGet,
			fmt.Sprintf("/repos/%s/%s/pulls?state=closed&sort=updated&direction=desc&per_page=100&page=%d",
				c.owner, c.repo, page), nil, &batch)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		stop := false
		for _, pr := range batch {
			if upd, perr := time.Parse(time.RFC3339, pr.UpdatedAt); perr == nil && upd.Before(since) {
				stop = true // sorted by updated desc — everything past here is older
				break
			}
			if pr.MergedAt == nil {
				continue // closed without merging
			}
			mergedAt, perr := time.Parse(time.RFC3339, *pr.MergedAt)
			if perr != nil || mergedAt.Before(since) {
				continue
			}
			out = append(out, MergedPR{Number: pr.Number, BaseRef: pr.Base.Ref, MergedAt: mergedAt})
		}
		if stop || len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// PRRef carries the base branch, head sha, and PR title/body — the fields the
// pull_request payload provides inline but an issue_comment payload omits. On
// the `@swatter review` re-trigger the runtime fetches these so the packet
// diffs against the right base and the reporter anchors to the current head.
type PRRef struct {
	BaseRef string
	HeadSHA string
	Title   string
	Body    string
}

// GetPR fetches a single pull request's base/head refs and title/body.
func (c *GitHubClient) GetPR(ctx context.Context, number int) (PRRef, error) {
	var pr struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/pulls/%d", c.owner, c.repo, number), nil, &pr)
	if err != nil {
		return PRRef{}, err
	}
	return PRRef{BaseRef: pr.Base.Ref, HeadSHA: pr.Head.SHA, Title: pr.Title, Body: pr.Body}, nil
}

// --- repository contents (the rule-book committer) ---

// ErrContentConflict marks a compare-and-swap failure on PutContent: the file
// changed on the branch between Get and Put. The committer refetches, re-applies
// its mutation, and retries.
var ErrContentConflict = fmt.Errorf("github contents: sha conflict")

// GetContent reads a file from a branch via the Contents API. found=false (with
// nil error) when the file does not exist yet.
func (c *GitHubClient) GetContent(ctx context.Context, path, ref string) (content, sha string, found bool, err error) {
	var res struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
	}
	status, err := c.doStatus(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", c.owner, c.repo, path, url.QueryEscape(ref)), nil, &res)
	if err != nil {
		if status == http.StatusNotFound {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	raw, decErr := base64.StdEncoding.DecodeString(strings.ReplaceAll(res.Content, "\n", ""))
	if decErr != nil {
		return "", "", false, fmt.Errorf("decode %s: %w", path, decErr)
	}
	return string(raw), res.SHA, true, nil
}

// PutContent creates or updates a file on a branch. sha is the blob sha from
// GetContent ("" for a new file) — GitHub rejects a stale sha, which is the
// compare-and-swap that makes the post-merge commit safe against a concurrent
// merge racing on the same file (returned as ErrContentConflict).
func (c *GitHubClient) PutContent(ctx context.Context, path, branch, message, content, sha string) error {
	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"branch":  branch,
	}
	if sha != "" {
		body["sha"] = sha
	}
	status, err := c.doStatus(ctx, http.MethodPut,
		fmt.Sprintf("/repos/%s/%s/contents/%s", c.owner, c.repo, path), body, nil)
	if err != nil && (status == http.StatusConflict || status == http.StatusUnprocessableEntity) {
		return fmt.Errorf("%w: %s (%v)", ErrContentConflict, path, err)
	}
	return err
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
