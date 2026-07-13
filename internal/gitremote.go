package internal

import (
	"context"
	"fmt"
	"strings"
)

// ParseRemoteURL extracts owner/repo from a git remote URL in either the SSH
// scp-like form (git@github.com:owner/repo.git), the ssh:// form
// (ssh://git@github.com/owner/repo), or the HTTPS form
// (https://github.com/owner/repo.git). A trailing ".git" and trailing slash are
// optional. It keys off the last two path segments, so an enterprise host
// (github.example.com) or a nested path parses too.
func ParseRemoteURL(remote string) (owner, repo string, err error) {
	s := strings.TrimSpace(remote)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")

	var path string
	switch {
	case strings.Contains(s, "://"):
		// scheme://[user@]host[:port]/owner/repo
		rest := s[strings.Index(s, "://")+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			path = rest[slash+1:]
		}
	case strings.Contains(s, ":"):
		// scp-like: git@host:owner/repo
		path = s[strings.LastIndex(s, ":")+1:]
	default:
		path = s
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] == "" || parts[len(parts)-1] == "" {
		return "", "", fmt.Errorf("cannot parse owner/repo from remote %q", remote)
	}
	return parts[len(parts)-2], parts[len(parts)-1], nil
}

// originRepo returns the owner/repo of the `origin` remote in repoRoot — the
// standalone stand-in for GITHUB_REPOSITORY.
func originRepo(ctx context.Context, repoRoot string) (owner, repo string, err error) {
	out, err := git(ctx, repoRoot, "remote", "get-url", "origin")
	if err != nil {
		return "", "", fmt.Errorf("git remote get-url origin: %w", err)
	}
	return ParseRemoteURL(out)
}

// defaultBranch returns the remote's default branch (the target of
// origin/HEAD), falling back to "main" when it can't be resolved — a fresh
// clone without origin/HEAD set, or no remote at all.
func defaultBranch(ctx context.Context, repoRoot string) string {
	if out, err := git(ctx, repoRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if ref := strings.TrimPrefix(strings.TrimSpace(out), "origin/"); ref != "" {
			return ref
		}
	}
	return "main"
}

// resolveDefaultBase picks the base ref for a default local review: the remote
// default branch (origin/<default>) when it exists, else the local branch of
// the same name. BuildPacket diffs base...HEAD (three-dot), so this yields the
// merge-base diff of the current branch against the default branch.
func resolveDefaultBase(ctx context.Context, repoRoot string) string {
	db := defaultBranch(ctx, repoRoot)
	for _, cand := range []string{"origin/" + db, db} {
		if _, err := git(ctx, repoRoot, "rev-parse", "--verify", "--quiet", cand); err == nil {
			return cand
		}
	}
	// Best effort: let BuildPacket surface a clear git error if neither exists.
	return "origin/" + db
}
