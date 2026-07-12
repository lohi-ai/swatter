// Package agentsmdcheck is a throwaway fixture for verifying convention-doc
// discovery. It is not wired into the build.
//
// Every exported identifier below deliberately OMITS a doc comment. Under the
// repo-root AGENTS.md that is a violation; under the repo-root CLAUDE.md it is
// compliant. The two docs contradict each other on purpose so that a Swatter
// conventions finding reveals which doc was in force.
package agentsmdcheck

// (no doc comments on any exported identifier below — on purpose)

func ExportedThing() string {
	return "hi"
}

func AnotherExportedThing(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += i
	}
	return total
}

func ThirdExportedThing(a, b string) string {
	return a + b
}

type ExportedConfig struct {
	Name    string
	Enabled bool
	Retries int
}

func NewExportedConfig(name string) *ExportedConfig {
	return &ExportedConfig{Name: name, Enabled: true, Retries: 3}
}

func (c *ExportedConfig) Describe() string {
	if c.Enabled {
		return c.Name + " (enabled)"
	}
	return c.Name + " (disabled)"
}

func (c *ExportedConfig) WithRetries(n int) *ExportedConfig {
	c.Retries = n
	return c
}
