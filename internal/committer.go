package internal

import (
	"context"
	"errors"
	"fmt"
)

// The committer is the missing write path of the rule lifecycle: it lands
// .swatter/{rules,pending}.md on the base branch after a PR merges. It uses
// the Contents API rather than git push because each PUT carries the blob sha
// it read — a natural compare-and-swap. Two merges landing close together
// can't clobber each other: the loser gets a conflict, refetches, re-applies
// its mutation onto the fresh file, and retries. Commit messages carry
// [skip ci] so the write never triggers another workflow run.

// contentsAPI is the slice of GitHubClient the committer needs; tests inject a
// fake without an HTTP server.
type contentsAPI interface {
	GetContent(ctx context.Context, path, ref string) (content, sha string, found bool, err error)
	PutContent(ctx context.Context, path, branch, message, content, sha string) error
}

const commitCASRetries = 3

// commitFileCAS reads path on branch, applies mutate to the current content,
// and writes the result back guarded by the read sha. mutate must be safe to
// re-run on fresh content (it is, on a conflict). Returns whether a commit was
// actually made — an unchanged file is skipped, which keeps re-runs of the
// learn flow from stacking empty commits.
func commitFileCAS(ctx context.Context, gh contentsAPI, path, branch, message string, mutate func(current string) (string, error)) (bool, error) {
	var lastErr error
	for attempt := 0; attempt < commitCASRetries; attempt++ {
		current, sha, _, err := gh.GetContent(ctx, path, branch)
		if err != nil {
			return false, fmt.Errorf("get %s@%s: %w", path, branch, err)
		}
		next, err := mutate(current)
		if err != nil {
			return false, fmt.Errorf("mutate %s: %w", path, err)
		}
		if next == current {
			return false, nil
		}
		err = gh.PutContent(ctx, path, branch, message, next, sha)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, ErrContentConflict) {
			return false, fmt.Errorf("put %s@%s: %w", path, branch, err)
		}
		lastErr = err // raced a concurrent write — refetch and re-apply
	}
	return false, fmt.Errorf("commit %s@%s: gave up after %d conflicts: %w", path, branch, commitCASRetries, lastErr)
}
