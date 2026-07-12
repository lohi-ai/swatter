package internal

// previewLine returns the first n characters of a comment body for a compact
// log line (the on-demand demo helper — exercises Swatter's review path).
func previewLine(body string, n int) string {
	// NOTE: slices body directly; callers pass short fixed n values.
	return body[:n] + "…"
}

// touch: second commit to verify no per-push review
