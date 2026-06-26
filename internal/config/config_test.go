package config

import (
	"os"
	"path/filepath"
	"testing"
)

// mustLoad calls Load and fails the test on error. Use it in tests that
// exercise valid/absent config; tests asserting a parse error call Load
// directly.
func mustLoad(t *testing.T) Config {
	t.Helper()
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}

func TestConfig_Default(t *testing.T) {
	cfg := mustLoad(t)
	if cfg.DB != "" {
		t.Errorf("default DB = %q, want empty", cfg.DB)
	}
}

func TestConfig_EnvVar(t *testing.T) {
	t.Setenv("CHRONCAL_DB", "/tmp/test-env.db")
	cfg := mustLoad(t)
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

	cfg := mustLoad(t)
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

	cfg := mustLoad(t)
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

	cfg := mustLoad(t)

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

	cfg := mustLoad(t)

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

	cfg := mustLoad(t)

	if cfg.Sync.Interval != "15m" {
		t.Fatalf("Sync.Interval = %q, want 15m", cfg.Sync.Interval)
	}
	if cfg.Sync.ConflictStrategy != "prompt" {
		t.Fatalf("Sync.ConflictStrategy = %q, want prompt", cfg.Sync.ConflictStrategy)
	}
}

func TestLoad_SecurityFromEnv(t *testing.T) {
	t.Setenv("CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_AUDIO_ATTACH", "true")
	t.Setenv("CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_EMAIL_ATTENDEES", "true")

	cfg := mustLoad(t)

	if !cfg.Security.AllowUnsafeAlarmAudioAttach {
		t.Fatal("Security.AllowUnsafeAlarmAudioAttach = false, want true")
	}
	if !cfg.Security.AllowUnsafeAlarmEmailAttendees {
		t.Fatal("Security.AllowUnsafeAlarmEmailAttendees = false, want true")
	}
}

func TestLoad_SecurityFromFile(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "chroncal")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[security]
allow_unsafe_alarm_audio_attach = true
allow_unsafe_alarm_email_attendees = true
`), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_AUDIO_ATTACH", "")
	t.Setenv("CHRONCAL_SECURITY_ALLOW_UNSAFE_ALARM_EMAIL_ATTENDEES", "")

	cfg := mustLoad(t)
	if !cfg.Security.AllowUnsafeAlarmAudioAttach {
		t.Fatal("Security.AllowUnsafeAlarmAudioAttach = false, want true")
	}
	if !cfg.Security.AllowUnsafeAlarmEmailAttendees {
		t.Fatal("Security.AllowUnsafeAlarmEmailAttendees = false, want true")
	}
}

func TestLoad_PurgeDaysDefaultsWhenUnset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config file
	t.Setenv("CHRONCAL_SOFT_DELETE_PURGE_DAYS", "")
	cfg := mustLoad(t)
	if cfg.SoftDelete.PurgeDays != DefaultSoftDeletePurgeDays {
		t.Errorf("PurgeDays = %d, want %d when unset", cfg.SoftDelete.PurgeDays, DefaultSoftDeletePurgeDays)
	}
}

func TestLoad_PurgeDaysZeroDisables(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "chroncal")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[soft_delete]\npurge_days = 0\n"), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CHRONCAL_SOFT_DELETE_PURGE_DAYS", "")

	cfg := mustLoad(t)
	if cfg.SoftDelete.PurgeDays != 0 {
		t.Errorf("PurgeDays = %d, want 0 (explicit 0 must stay disabled, not default to %d)",
			cfg.SoftDelete.PurgeDays, DefaultSoftDeletePurgeDays)
	}
}

func TestLoad_PurgeDaysExplicitValue(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "chroncal")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[soft_delete]\npurge_days = 7\n"), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CHRONCAL_SOFT_DELETE_PURGE_DAYS", "")

	cfg := mustLoad(t)
	if cfg.SoftDelete.PurgeDays != 7 {
		t.Errorf("PurgeDays = %d, want 7", cfg.SoftDelete.PurgeDays)
	}
}

func TestLoad_SMTPPortDefaultsWhenUnset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config file
	t.Setenv("CHRONCAL_SMTP_PORT", "")
	cfg := mustLoad(t)
	if cfg.SMTP.Port != DefaultSMTPPort {
		t.Errorf("SMTP.Port = %d, want %d when unset", cfg.SMTP.Port, DefaultSMTPPort)
	}
}

// TestLoad_MalformedFileReturnsError guards against silently swallowing a
// syntax error in config.toml. A broken file must surface an error rather than
// be treated like an absent file, which would revert db/security/etc. to
// defaults and risk opening the wrong (default) database.
func TestLoad_MalformedFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "chroncal")
	os.MkdirAll(configDir, 0o755)
	// Valid db key but a syntax error elsewhere (unterminated string).
	content := "db = \"/home/me/work.db\"\nproduct_id = \"oops\n"
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CHRONCAL_DB", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want a parse error for malformed config.toml")
	}
}

// TestLoad_NoFileNoError confirms an absent config file is still treated as
// optional (no error), distinguishing "no file" from "broken file".
func TestLoad_NoFileNoError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config file present
	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v, want nil when config file is absent", err)
	}
}
