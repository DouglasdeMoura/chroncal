package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfig_Default(t *testing.T) {
	cfg := Load()
	if cfg.DB != "" {
		t.Errorf("default DB = %q, want empty", cfg.DB)
	}
}

func TestConfig_EnvVar(t *testing.T) {
	t.Setenv("TCAL_DB", "/tmp/test-env.db")
	cfg := Load()
	if cfg.DB != "/tmp/test-env.db" {
		t.Errorf("DB = %q, want %q", cfg.DB, "/tmp/test-env.db")
	}
}

func TestConfig_File(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "tcal")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`db = "/tmp/test-file.db"`), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("TCAL_DB", "")

	cfg := Load()
	if cfg.DB != "/tmp/test-file.db" {
		t.Errorf("DB = %q, want %q", cfg.DB, "/tmp/test-file.db")
	}
}

func TestConfig_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "tcal")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`db = "/tmp/from-file.db"`), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("TCAL_DB", "/tmp/from-env.db")

	cfg := Load()
	if cfg.DB != "/tmp/from-env.db" {
		t.Errorf("DB = %q, want %q (env should override file)", cfg.DB, "/tmp/from-env.db")
	}
}
