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

// ValidatorPrompt is the Soul for a validator run.
func ValidatorPrompt() string { return mustSkill("validator.md") }

// LearnPrompt is the Soul for the rule-learning run.
func LearnPrompt() string { return mustSkill("learn.md") }

// FeedbackClusterPrompt is the Soul for the post-merge observation-clustering
// run that proposes rules from accumulated feedback evidence.
func FeedbackClusterPrompt() string { return mustSkill("feedback_cluster.md") }

// AngleCharter returns the charter markdown for a finder angle letter (A–H),
// used as the AgentDefinition.Agents slot. Unknown letters return "".
func AngleCharter(letter string) string {
	switch letter {
	case "A", "B", "C", "D", "E", "F", "G", "H":
		return mustSkill("angle_" + letter + ".md")
	default:
		return ""
	}
}

// AllAngles is the review-pr angle set in dispatch order.
var AllAngles = []string{"A", "B", "C", "D", "E", "F", "G", "H"}
