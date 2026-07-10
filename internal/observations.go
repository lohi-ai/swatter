package internal

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The pending-observations ledger (.swatter/pending.md) is the conservative
// gate between feedback and new rules: one confirmed signal is an anecdote, so
// it is recorded here instead of minting a rule. Only when same-pattern
// observations accumulate enough weight across distinct PRs does the
// clustering pass promote them into .swatter/rules.md. The ledger is committed
// next to the rule book so evidence survives across PRs and CI runs.

// ObsKind says where an observation came from, which sets its weight.
type ObsKind string

const (
	// ObsRepeat: a swatter finding humans confirmed valuable that no existing
	// rule produced. Weight 1 — it takes repetition to prove a pattern.
	ObsRepeat ObsKind = "repeat"
	// ObsMissed: a bug another reviewer caught (and the author acted on) that
	// swatter did not report. Weight 2 — a hole in coverage is stronger
	// evidence than a repeat, but one occurrence still doesn't mint a rule.
	ObsMissed ObsKind = "missed"
)

// Observation is one piece of evidence that the rule book has a gap.
type Observation struct {
	ID   string
	Kind ObsKind
	PR   int
	Date string // YYYY-MM-DD
	Path string
	Note string // one line: the finding summary or the reviewer's comment
}

// Weight is the observation's contribution toward the promotion threshold.
func (o Observation) Weight() int {
	if o.Kind == ObsMissed {
		return 2
	}
	return 1
}

// Ledger retention: observations age out — an issue that stops recurring is
// not a pattern — and the file stays small enough to commit forever.
const (
	obsMaxAgeDays = 120
	obsMaxEntries = 60
)

// ObsLedger is the parsed pending file plus its operations.
type ObsLedger struct {
	Obs []Observation
}

// ParseObsLedger reads the markdown ledger. Unknown lines are ignored, same
// tolerance as ParseRuleStore.
func ParseObsLedger(md string) *ObsLedger {
	l := &ObsLedger{}
	var cur *Observation
	flush := func() {
		if cur != nil && strings.TrimSpace(cur.Note) != "" {
			l.Obs = append(l.Obs, *cur)
		}
		cur = nil
	}
	for _, raw := range strings.Split(md, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "- id:") {
			flush()
			cur = &Observation{ID: strings.TrimSpace(strings.TrimPrefix(line, "- id:")), Kind: ObsRepeat}
			continue
		}
		if cur == nil {
			continue
		}
		// note is free text (may contain colons/spacing) — take the whole rest.
		if rest, ok := strings.CutPrefix(line, "note:"); ok {
			cur.Note = strings.TrimSpace(rest)
			continue
		}
		for _, field := range strings.Split(line, "   ") { // 3-space separated, as rules.md
			k, v, ok := strings.Cut(strings.TrimSpace(field), ":")
			if !ok {
				continue
			}
			switch strings.TrimSpace(k) {
			case "kind":
				cur.Kind = ObsKind(strings.TrimSpace(v))
			case "pr":
				cur.PR, _ = strconv.Atoi(strings.TrimSpace(v))
			case "date":
				cur.Date = strings.TrimSpace(v)
			case "path":
				cur.Path = strings.TrimSpace(v)
			}
		}
	}
	flush()
	return l
}

// Render serializes the ledger in the canonical format.
func (l *ObsLedger) Render() string {
	if len(l.Obs) == 0 {
		return "# Swatter pending observations\n\n_No pending observations._\n"
	}
	var b strings.Builder
	b.WriteString("# Swatter pending observations\n\n")
	b.WriteString("<!-- Managed by Swatter: evidence collected from merged-PR feedback. Entries are promoted into rules.md when a pattern repeats, and age out otherwise. -->\n\n")
	for _, o := range l.Obs {
		fmt.Fprintf(&b, "- id: %s\n", o.ID)
		fmt.Fprintf(&b, "  kind: %s\n", o.Kind)
		fmt.Fprintf(&b, "  pr: %d   date: %s\n", o.PR, o.Date)
		if o.Path != "" {
			fmt.Fprintf(&b, "  path: %s\n", o.Path)
		}
		fmt.Fprintf(&b, "  note: %s\n", o.Note)
	}
	return b.String()
}

// Add appends an observation, assigning an id. Re-processing the same PR (a
// re-run workflow) is a no-op: an entry with the same PR and normalized note
// already exists. Returns whether the observation was actually added.
func (l *ObsLedger) Add(o Observation) bool {
	o.Note = collapseSpaces(oneLine(o.Note))
	if o.Note == "" {
		return false
	}
	norm := normalizeRule(o.Note)
	for _, e := range l.Obs {
		if e.PR == o.PR && normalizeRule(e.Note) == norm {
			return false
		}
	}
	o.ID = fmt.Sprintf("o-%s-%d", o.Date, l.nextSeq())
	l.Obs = append(l.Obs, o)
	return true
}

// nextSeq returns 1 + the highest numeric id suffix, so ids stay unique even
// after removals.
func (l *ObsLedger) nextSeq() int {
	max := 0
	for _, o := range l.Obs {
		if i := strings.LastIndexByte(o.ID, '-'); i >= 0 {
			if n, err := strconv.Atoi(o.ID[i+1:]); err == nil && n > max {
				max = n
			}
		}
	}
	return max + 1
}

// Get returns the observation with the given id.
func (l *ObsLedger) Get(id string) (Observation, bool) {
	for _, o := range l.Obs {
		if o.ID == id {
			return o, true
		}
	}
	return Observation{}, false
}

// Remove drops the given ids — used after their cluster is promoted to a rule.
func (l *ObsLedger) Remove(ids []string) {
	drop := toSet(ids)
	kept := l.Obs[:0]
	for _, o := range l.Obs {
		if !drop[o.ID] {
			kept = append(kept, o)
		}
	}
	l.Obs = kept
}

// Prune ages out stale observations and caps the ledger at the newest entries.
func (l *ObsLedger) Prune(now time.Time) {
	kept := l.Obs[:0]
	for _, o := range l.Obs {
		if t, err := time.Parse("2006-01-02", o.Date); err == nil {
			if now.Sub(t).Hours()/24 > obsMaxAgeDays {
				continue
			}
		}
		kept = append(kept, o)
	}
	l.Obs = kept
	if len(l.Obs) > obsMaxEntries {
		// Keep the newest by date (stable within a day: later entries win).
		sort.SliceStable(l.Obs, func(i, j int) bool { return l.Obs[i].Date > l.Obs[j].Date })
		l.Obs = l.Obs[:obsMaxEntries]
		sort.SliceStable(l.Obs, func(i, j int) bool { return l.Obs[i].Date < l.Obs[j].Date })
	}
}

// ClusterEvidence checks a proposed cluster against the ledger: it keeps only
// member ids that actually exist, and returns their total weight and how many
// distinct PRs they span. The promotion decision is the harness's — the
// clustering model proposes, this verifies.
func (l *ObsLedger) ClusterEvidence(memberIDs []string) (weight, distinctPRs int, valid []string) {
	prs := map[int]bool{}
	for _, id := range memberIDs {
		o, ok := l.Get(id)
		if !ok {
			continue
		}
		valid = append(valid, id)
		weight += o.Weight()
		prs[o.PR] = true
	}
	return weight, len(prs), valid
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
