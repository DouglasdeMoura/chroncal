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
	t.Setenv("CHRONCAL_DB", "/tmp/test-env.db")
	cfg := Load()
	if cfg.DB != "/tmp/test-env.db" {
		t.Errorf("DB = %q, want %q", cfg.DB, "/tmp/test-env.db")
	}
}

func TestConfig_File(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "chroncal")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`db = "/tmp/test-file.db"`), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CHRONCAL_DB", "")

	cfg := Load()
	if cfg.DB != "/tmp/test-file.db" {
		t.Errorf("DB = %q, want %q", cfg.DB, "/tmp/test-file.db")
	}
}

func TestConfig_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "chroncal")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`db = "/tmp/from-file.db"`), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CHRONCAL_DB", "/tmp/from-env.db")

	cfg := Load()
	if cfg.DB != "/tmp/from-env.db" {
		t.Errorf("DB = %q, want %q (env should override file)", cfg.DB, "/tmp/from-env.db")
	}
}

func TestLoad_SMTPFromFile(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "chroncal")
	os.MkdirAll(configDir, 0o755)

	content := `db = "/tmp/test.db"

[smtp]
host = "smtp.example.com"
port = 587
username = "user@example.com"
password = "secret123"
from = "noreply@example.com"
`
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	// Clear SMTP env vars so they don't interfere
	t.Setenv("CHRONCAL_SMTP_HOST", "")
	t.Setenv("CHRONCAL_SMTP_PORT", "")
	t.Setenv("CHRONCAL_SMTP_USERNAME", "")
	t.Setenv("CHRONCAL_SMTP_PASSWORD", "")
	t.Setenv("CHRONCAL_SMTP_FROM", "")

	cfg := Load()

	if cfg.SMTP.Host != "smtp.example.com" {
		t.Errorf("SMTP.Host = %q, want %q", cfg.SMTP.Host, "smtp.example.com")
	}
	if cfg.SMTP.Port != 587 {
		t.Errorf("SMTP.Port = %d, want %d", cfg.SMTP.Port, 587)
	}
	if cfg.SMTP.Username != "user@example.com" {
		t.Errorf("SMTP.Username = %q, want %q", cfg.SMTP.Username, "user@example.com")
	}
	if cfg.SMTP.Password != "secret123" {
		t.Errorf("SMTP.Password = %q, want %q", cfg.SMTP.Password, "secret123")
	}
	if cfg.SMTP.From != "noreply@example.com" {
		t.Errorf("SMTP.From = %q, want %q", cfg.SMTP.From, "noreply@example.com")
	}
}

func TestLoad_NerdFontsFromFile(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "chroncal")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`nerd_fonts = true`), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CHRONCAL_NERD_FONTS", "")

	cfg := Load()
	if !cfg.NerdFonts {
		t.Error("NerdFonts = false, want true")
	}
}

func TestLoad_NerdFontsFromEnv(t *testing.T) {
	t.Setenv("CHRONCAL_NERD_FONTS", "true")

	cfg := Load()
	if !cfg.NerdFonts {
		t.Error("NerdFonts = false, want true (from env)")
	}
}

func TestLoad_NerdFontsDefault(t *testing.T) {
	t.Setenv("CHRONCAL_NERD_FONTS", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg := Load()
	if cfg.NerdFonts {
		t.Error("NerdFonts = true, want false (default)")
	}
}

func TestLoad_SMTPFromEnv(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "chroncal")
	os.MkdirAll(configDir, 0o755)

	// Write a file with different SMTP values to verify env overrides
	content := `[smtp]
host = "file-host.example.com"
port = 25
`
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CHRONCAL_SMTP_HOST", "env-host.example.com")
	t.Setenv("CHRONCAL_SMTP_PORT", "465")

	cfg := Load()

	if cfg.SMTP.Host != "env-host.example.com" {
		t.Errorf("SMTP.Host = %q, want %q (env should override file)", cfg.SMTP.Host, "env-host.example.com")
	}
	if cfg.SMTP.Port != 465 {
		t.Errorf("SMTP.Port = %d, want %d (env should override file)", cfg.SMTP.Port, 465)
	}
}

func TestLoad_SyncFromEnv(t *testing.T) {
	t.Setenv("CHRONCAL_SYNC_INTERVAL", "15m")
	t.Setenv("CHRONCAL_SYNC_CONFLICT_STRATEGY", "prompt")

	cfg := Load()

	if cfg.Sync.Interval != "15m" {
		t.Fatalf("Sync.Interval = %q, want 15m", cfg.Sync.Interval)
	}
	if cfg.Sync.ConflictStrategy != "prompt" {
		t.Fatalf("Sync.ConflictStrategy = %q, want prompt", cfg.Sync.ConflictStrategy)
	}
}
