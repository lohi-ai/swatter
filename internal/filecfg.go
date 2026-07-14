package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// configKey maps a user-facing config name to the SWATTER_* environment
// variable it fills. The standalone `swatter config` command reads and writes
// these to a file on the user's machine; the file is layered *under* the
// environment (see applyConfigFileDefaults), so CI — which sets the env and
// ships no file — is never affected.
type configKey struct {
	name   string // user-facing key, e.g. "api-key"
	env    string // SWATTER_* variable it fills
	secret bool   // redacted in `config list`
}

// configKeys is the set a standalone user needs to run a review without
// exporting SWATTER_* by hand. It is deliberately a subset of LoadConfig's
// inputs — the budget/lifecycle knobs stay env-only (advanced, CI-oriented).
var configKeys = []configKey{
	{"provider", "SWATTER_PROVIDER", false},
	{"api-key", "SWATTER_API_KEY", true},
	{"base-url", "SWATTER_BASE_URL", false},
	{"model", "SWATTER_MODEL", false},
	{"model-cheap", "SWATTER_MODEL_CHEAP", false},
	{"effort", "SWATTER_EFFORT", false},
	{"fail-on", "SWATTER_FAIL_ON", false},
	{"github-token", "SWATTER_GITHUB_TOKEN", true},
	{"resolve-token", "SWATTER_RESOLVE_TOKEN", true},
}

// lookupConfigKey resolves a user key to its mapping, normalizing case and
// treating `_` and `-` as equivalent (so `fail_on` and `fail-on` both work).
func lookupConfigKey(name string) (configKey, bool) {
	n := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "_", "-"))
	for _, k := range configKeys {
		if k.name == n {
			return k, true
		}
	}
	return configKey{}, false
}

// knownConfigKeys is the comma-joined key list for error messages.
func knownConfigKeys() string {
	names := make([]string, len(configKeys))
	for i, k := range configKeys {
		names[i] = k.name
	}
	return strings.Join(names, ", ")
}

// redactConfigValue hides secret values in `config list` — the key stays
// visible so the user can confirm it is set, but the value never prints.
func redactConfigValue(k configKey, v string) string {
	if k.secret && strings.TrimSpace(v) != "" {
		return "set (hidden)"
	}
	return v
}

// ConfigFilePath is the standalone config location:
// $XDG_CONFIG_HOME/swatter/config.json, or ~/.config/swatter/config.json. It
// can hold the API key (written 0600 — the user's own machine), so a local
// trial never needs SWATTER_* exported by hand.
func ConfigFilePath() (string, error) {
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "swatter", "config.json"), nil
}

// loadConfigFile reads the standalone config file as name→value. A missing or
// unreadable file yields nil and no error: the file is an optional convenience
// layered under the environment, never a hard dependency.
func loadConfigFile() map[string]string {
	path, err := ConfigFilePath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

// saveConfigFile writes the standalone config file 0600 (parent dir 0700).
func saveConfigFile(m map[string]string) error {
	path, err := ConfigFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	// WriteFile only applies the mode when creating the file; tighten an
	// existing file too, since it can hold the API key / tokens.
	return os.Chmod(path, 0o600)
}

// applyConfigFileDefaults layers the standalone config file *under* the
// environment: for every mapped key whose SWATTER_* variable is unset, the
// file's value fills it. The environment always wins, so a CI run (env set, no
// file) is byte-for-byte unchanged, while a local run with only a config file
// resolves the same Config it would from exporting the env by hand. Best-effort
// and idempotent — a missing file is the common case and a no-op.
func applyConfigFileDefaults() {
	file := loadConfigFile()
	if len(file) == 0 {
		return
	}
	for _, k := range configKeys {
		if strings.TrimSpace(os.Getenv(k.env)) != "" {
			continue // env wins
		}
		if v, ok := file[k.name]; ok && strings.TrimSpace(v) != "" {
			_ = os.Setenv(k.env, v)
		}
	}
}
