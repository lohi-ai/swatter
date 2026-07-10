package internal

import (
	"fmt"
	"strings"
	"sync"
)

// StickyMarker is the hidden HTML comment that identifies swatter's sticky
// comment so re-pushes update it in place (idempotency).
const StickyMarker = "<!-- swatter:sticky -->"

// ProgressTracker accumulates phase notes and renders the live sticky-comment
// body — the "[x] packet → [x] finders → [ ] validators" experience stolen
// from claude-code-action. Thread-safe: the pipeline emits notes from parallel
// goroutines.
type ProgressTracker struct {
	mu    sync.Mutex
	notes []string
	done  bool
}

// Note appends a progress line.
func (t *ProgressTracker) Note(s string) {
	t.mu.Lock()
	t.notes = append(t.notes, s)
	t.mu.Unlock()
}

// RenderLive returns the in-progress sticky body (checklist of notes so far).
func (t *ProgressTracker) RenderLive() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	b.WriteString(StickyMarker + "\n")
	b.WriteString("### 🪰 Swatter is reviewing…\n\n")
	for _, n := range t.notes {
		fmt.Fprintf(&b, "- [x] %s\n", n)
	}
	b.WriteString("- [ ] reporting\n")
	return b.String()
}

// RenderFinal wraps a finished summary body with the sticky marker.
func RenderFinal(summary string) string {
	return StickyMarker + "\n" + summary
}
