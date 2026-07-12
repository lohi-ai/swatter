package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// GitHubEvent is the subset of a pull_request / issue_comment webhook payload
// swatter needs to build a packet and report back. Decoded from the file at
// $GITHUB_EVENT_PATH that the Actions runner writes.
type GitHubEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		// Merged + MergeCommitSHA are set on the `closed` action and drive the
		// post-merge feedback/learn flow (a closed-unmerged PR is skipped).
		Merged         bool   `json:"merged"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
				Fork     bool   `json:"fork"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
	} `json:"pull_request"`
	// issue_comment payloads carry the PR under "issue" + the comment body.
	Issue struct {
		Number      int `json:"number"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		Body string `json:"body"`
		// AuthorAssociation gates who may re-trigger a review by mention. The
		// workflow `if:` is the primary guard; this is the runtime backstop.
		AuthorAssociation string `json:"author_association"`
	} `json:"comment"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// LoadEvent decodes the webhook payload at path (typically $GITHUB_EVENT_PATH).
func LoadEvent(path string) (*GitHubEvent, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read event %q: %w", path, err)
	}
	var e GitHubEvent
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("decode event: %w", err)
	}
	return &e, nil
}

// IsFork reports whether the PR originates from a fork, where the Action's
// GITHUB_TOKEN is read-only and secrets are unavailable — swatter exits neutral
// rather than posting a failing check it can't attach comments to.
func (e *GitHubEvent) IsFork() bool {
	return e.PullRequest.Head.Repo.Fork
}

// IsMergedClose reports whether this event is a pull_request `closed` action
// for a PR that actually merged — the trigger for the feedback/learn flow.
func (e *GitHubEvent) IsMergedClose() bool {
	return e.Action == "closed" && e.PullRequest.Merged
}

// IsIssueComment reports whether this payload is a comment on a pull request
// (the on-demand `@swatter review` re-trigger path). Issue comments on plain
// issues carry no PullRequest link and are ignored.
func (e *GitHubEvent) IsIssueComment() bool {
	return e.Issue.PullRequest != nil
}

// ReviewMentioned reports whether a comment body asks swatter to review, i.e.
// contains "@swatter review" (case-insensitive). The workflow already filters
// on this; the runtime re-checks so a mis-wired trigger can't burn a review.
func (e *GitHubEvent) ReviewMentioned() bool {
	return strings.Contains(strings.ToLower(e.Comment.Body), "@swatter review")
}

// PRNumber returns the pull-request number from either a pull_request or an
// issue_comment payload.
func (e *GitHubEvent) PRNumber() int {
	if e.PullRequest.Number != 0 {
		return e.PullRequest.Number
	}
	if e.Number != 0 {
		return e.Number
	}
	return e.Issue.Number
}
