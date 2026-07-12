package internal

import (
	"strings"
	"testing"
)

func TestReviewMentioned(t *testing.T) {
	cases := map[string]bool{
		"@swatter review":             true,
		"please @swatter review this": true,
		"@Swatter Review":             true, // case-insensitive
		"@swatter please review":      false,
		"looks good, merging":         false,
	}
	for body, want := range cases {
		e := &GitHubEvent{}
		e.Comment.Body = body
		if got := e.ReviewMentioned(); got != want {
			t.Errorf("ReviewMentioned(%q) = %v, want %v", body, got, want)
		}
	}
}

func TestIsIssueComment(t *testing.T) {
	pr := &GitHubEvent{}
	if pr.IsIssueComment() {
		t.Fatal("a bare pull_request payload is not an issue comment")
	}
	c := &GitHubEvent{}
	c.Issue.Number = 7
	c.Issue.PullRequest = &struct {
		URL string `json:"url"`
	}{URL: "https://api.github.com/repos/o/r/pulls/7"}
	if !c.IsIssueComment() {
		t.Fatal("a comment carrying a pull_request link is an issue comment")
	}
	if c.PRNumber() != 7 {
		t.Fatalf("PRNumber() = %d, want 7", c.PRNumber())
	}
}

func TestRenderWorkflowMode(t *testing.T) {
	od := renderWorkflow("openai-compat", "pro", "https://x/v1", modeOnDemand)
	if !strings.Contains(od, "issue_comment") || !strings.Contains(od, "@swatter review") {
		t.Error("on-demand workflow must trigger on the @swatter review comment")
	}
	if strings.Contains(od, "synchronize") {
		t.Error("on-demand workflow must not review on every push (synchronize)")
	}
	if !strings.Contains(od, "author_association") {
		t.Error("on-demand workflow must gate the mention on commenter permission")
	}

	pc := renderWorkflow("anthropic", "claude-opus-4-8", "", modePerCommit)
	if !strings.Contains(pc, "synchronize") {
		t.Error("per-commit workflow must review on every push")
	}
	if strings.Contains(pc, "issue_comment") {
		t.Error("per-commit workflow should not add a comment trigger")
	}
}
