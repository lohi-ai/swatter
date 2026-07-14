package internal

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfigFile points XDG_CONFIG_HOME at a temp dir and writes a config file
// there, so config tests never touch the developer's real ~/.config/swatter.
func writeConfigFile(t *testing.T, json string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "swatter", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(json), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigFileRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveConfigFile(map[string]string{"api-key": "sk-1", "model": "m"}); err != nil {
		t.Fatal(err)
	}
	path, _ := ConfigFilePath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file perm = %o, want 600", perm)
	}
	got := loadConfigFile()
	if got["api-key"] != "sk-1" || got["model"] != "m" {
		t.Errorf("loadConfigFile() = %v", got)
	}
}

func TestApplyConfigFileDefaults_EnvWins(t *testing.T) {
	writeConfigFile(t, `{"api-key":"filekey","model":"filemodel","provider":"anthropic"}`)
	t.Setenv("SWATTER_API_KEY", "envkey") // env set → must win
	t.Setenv("SWATTER_MODEL", "")         // unset → file fills
	t.Setenv("SWATTER_PROVIDER", "")      // unset → file fills

	applyConfigFileDefaults()

	if got := os.Getenv("SWATTER_API_KEY"); got != "envkey" {
		t.Errorf("api-key = %q, want envkey (env must win)", got)
	}
	if got := os.Getenv("SWATTER_MODEL"); got != "filemodel" {
		t.Errorf("model = %q, want filemodel (file fills gap)", got)
	}
	if got := os.Getenv("SWATTER_PROVIDER"); got != "anthropic" {
		t.Errorf("provider = %q, want anthropic (file fills gap)", got)
	}
}

func TestLoadConfig_FileUnderEnv(t *testing.T) {
	writeConfigFile(t, `{"api-key":"filekey","model":"filemodel"}`)
	// Clear the SWATTER_* the file provides so the file is the only source.
	t.Setenv("SWATTER_API_KEY", "")
	t.Setenv("SWATTER_MODEL", "")
	t.Setenv("SWATTER_PROVIDER", "")
	t.Setenv("SWATTER_BASE_URL", "")
	t.Setenv("SWATTER_EFFORT", "")

	c, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig from file: %v", err)
	}
	if c.APIKey != "filekey" || c.ModelStrong != "filemodel" {
		t.Fatalf("LoadConfig = key %q model %q; want filekey/filemodel", c.APIKey, c.ModelStrong)
	}

	// Env overrides the file.
	t.Setenv("SWATTER_MODEL", "envmodel")
	c, err = LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.ModelStrong != "envmodel" {
		t.Fatalf("LoadConfig model = %q, want envmodel (env overrides file)", c.ModelStrong)
	}
}

func TestLookupConfigKey(t *testing.T) {
	if k, ok := lookupConfigKey("fail_on"); !ok || k.name != "fail-on" {
		t.Errorf("lookupConfigKey(fail_on) = %+v,%v; want fail-on", k, ok)
	}
	if k, ok := lookupConfigKey("API-KEY"); !ok || k.name != "api-key" {
		t.Errorf("lookupConfigKey(API-KEY) = %+v,%v; want api-key", k, ok)
	}
	if _, ok := lookupConfigKey("nonsense"); ok {
		t.Error("lookupConfigKey(nonsense) should not resolve")
	}
}

func TestRedactConfigValue(t *testing.T) {
	secret := configKey{name: "api-key", secret: true}
	if got := redactConfigValue(secret, "sk-123"); got != "set (hidden)" {
		t.Errorf("redact secret = %q, want 'set (hidden)'", got)
	}
	if got := redactConfigValue(secret, ""); got != "" {
		t.Errorf("redact empty secret = %q, want empty", got)
	}
	pub := configKey{name: "provider"}
	if got := redactConfigValue(pub, "anthropic"); got != "anthropic" {
		t.Errorf("redact public = %q, want anthropic", got)
	}
}
