package internal

import (
	"embed"
	"fmt"
)

//go:embed skills/*.md
var skillsFS embed.FS

func mustSkill(name string) string {
	b, err := skillsFS.ReadFile("skills/" + name)
	if err != nil {
		// Embedded at build time; a missing file is a programmer error.
		panic(fmt.Sprintf("swatter: embedded skill %q missing: %v", name, err))
	}
	return string(b)
}

// FinderPreamble is the always-loaded Soul for every finder run.
func FinderPreamble() string { return mustSkill("finder_preamble.md") }

// ValidatorPrompt is the Soul for a validator (per-location verifier) run.
func ValidatorPrompt() string { return mustSkill("validator.md") }

// ScopePrompt is the Soul for the scope run that pins the change summary and the
// applicable convention-doc rules (AGENTS.md, else CLAUDE.md) the finders share.
func ScopePrompt() string { return mustSkill("scope.md") }

// CleanupCharter is the Agents charter for the single cleanup finder that covers
// all five cleanup lenses (reuse / simplification / efficiency / altitude /
// conventions), per the reference's one-agent cleanup design.
func CleanupCharter() string { return mustSkill("cleanup.md") }

// SweepCharter is the Agents charter for the gap-focused sweep finder.
func SweepCharter() string { return mustSkill("sweep.md") }

// SynthesizePrompt is the Soul for the synthesis run that ranks, merges, and
// caps the verified findings by index.
func SynthesizePrompt() string { return mustSkill("synthesize.md") }

// LearnPrompt is the Soul for the rule-learning run.
func LearnPrompt() string { return mustSkill("learn.md") }

// FeedbackClusterPrompt is the Soul for the post-merge observation-clustering
// run that proposes rules from accumulated feedback evidence.
func FeedbackClusterPrompt() string { return mustSkill("feedback_cluster.md") }

// AngleCharter returns the charter markdown for a correctness finder angle
// letter (A–E), used as the AgentDefinition.Agents slot. Unknown letters return
// "". The cleanup lenses are one separate agent — see CleanupCharter.
func AngleCharter(letter string) string {
	switch letter {
	case "A", "B", "C", "D", "E":
		return mustSkill("angle_" + letter + ".md")
	default:
		return ""
	}
}

// CorrectnessAngles is the reference A–E correctness set, one finder agent each.
var CorrectnessAngles = []string{"A", "B", "C", "D", "E"}

// AngleCleanup / AngleSweep are the pseudo-angle labels the harness tags on
// candidates from the single cleanup finder and the sweep pass, so the ANGLES
// summary line and per-candidate attribution stay uniform with A–E.
const (
	AngleCleanup = "cleanup"
	AngleSweep   = "sweep"
)

// AllAngles is every angle bucket in report order: the A–E correctness angles
// plus the cleanup bucket. Sweep is reported on its own footer field, not here.
var AllAngles = append(append([]string{}, CorrectnessAngles...), AngleCleanup)
