package internal

import (
	"strconv"
	"strings"
)

// DiffMap records, per file path, the set of new-file line numbers that appear
// in the diff (added or context lines). GitHub only accepts an inline review
// comment on a line present in the diff on the RIGHT side; a finding on a line
// outside any hunk must fall back to the summary comment. Built from the same
// unified diff the packet carries, so the reporter needs no extra git call.
type DiffMap struct {
	commentable map[string]map[int]bool
}

// BuildDiffMap parses a unified diff (git diff --no-color output).
func BuildDiffMap(diff string) *DiffMap {
	m := &DiffMap{commentable: map[string]map[int]bool{}}
	var path string
	var newLine int
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			path = stripDiffPrefix(strings.TrimPrefix(line, "+++ "))
			inHunk = false
		case strings.HasPrefix(line, "diff --git"):
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			// @@ -a,b +c,d @@ — c is the new-file start line.
			newLine = parseHunkNewStart(line)
			inHunk = true
		case inHunk && path != "":
			switch {
			case strings.HasPrefix(line, "+"):
				m.mark(path, newLine)
				newLine++
			case strings.HasPrefix(line, "-"):
				// deleted line: no new-file line number, not commentable on RIGHT
			case strings.HasPrefix(line, "\\"):
				// "\ No newline at end of file" — not a content line
			default:
				// context line: present on the new side, commentable
				m.mark(path, newLine)
				newLine++
			}
		}
	}
	return m
}

// Commentable reports whether (path, line) can carry an inline RIGHT comment.
func (m *DiffMap) Commentable(path string, line int) bool {
	set, ok := m.commentable[path]
	if !ok {
		return false
	}
	return set[line]
}

func (m *DiffMap) mark(path string, line int) {
	if line <= 0 {
		return
	}
	if m.commentable[path] == nil {
		m.commentable[path] = map[int]bool{}
	}
	m.commentable[path][line] = true
}

// stripDiffPrefix removes the "b/" (or "a/") prefix git adds to diff paths and
// handles the "/dev/null" sentinel.
func stripDiffPrefix(p string) string {
	p = strings.TrimSpace(p)
	if p == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

// parseHunkNewStart extracts c from "@@ -a,b +c,d @@ ...".
func parseHunkNewStart(hunk string) int {
	plus := strings.IndexByte(hunk, '+')
	if plus < 0 {
		return 0
	}
	rest := hunk[plus+1:]
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		end = len(rest)
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}
