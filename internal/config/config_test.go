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

func TestLoad_SMTPFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	content := `db = "/tmp/test.db"

[smtp]
host = "smtp.example.com"
port = 587
username = "user@example.com"
password = "secret123"
from = "noreply@example.com"
`
	os.WriteFile(path, []byte(content), 0o644)

	// Clear SMTP env vars so they don't interfere
	t.Setenv("TCAL_SMTP_HOST", "")
	t.Setenv("TCAL_SMTP_PORT", "")
	t.Setenv("TCAL_SMTP_USERNAME", "")
	t.Setenv("TCAL_SMTP_PASSWORD", "")
	t.Setenv("TCAL_SMTP_FROM", "")

	cfg := LoadFile(path)

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
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`nerd_fonts = true`), 0o644)

	t.Setenv("TCAL_NERD_FONTS", "")

	cfg := LoadFile(path)
	if !cfg.NerdFonts {
		t.Error("NerdFonts = false, want true")
	}
}

func TestLoad_NerdFontsFromEnv(t *testing.T) {
	t.Setenv("TCAL_NERD_FONTS", "true")

	cfg := Load()
	if !cfg.NerdFonts {
		t.Error("NerdFonts = false, want true (from env)")
	}
}

func TestLoad_NerdFontsDefault(t *testing.T) {
	t.Setenv("TCAL_NERD_FONTS", "")

	cfg := Load()
	if cfg.NerdFonts {
		t.Error("NerdFonts = true, want false (default)")
	}
}

func TestLoad_SMTPFromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Write a file with different SMTP values to verify env overrides
	content := `[smtp]
host = "file-host.example.com"
port = 25
`
	os.WriteFile(path, []byte(content), 0o644)

	t.Setenv("TCAL_SMTP_HOST", "env-host.example.com")
	t.Setenv("TCAL_SMTP_PORT", "465")

	cfg := LoadFile(path)

	if cfg.SMTP.Host != "env-host.example.com" {
		t.Errorf("SMTP.Host = %q, want %q (env should override file)", cfg.SMTP.Host, "env-host.example.com")
	}
	if cfg.SMTP.Port != 465 {
		t.Errorf("SMTP.Port = %d, want %d (env should override file)", cfg.SMTP.Port, 465)
	}
}
