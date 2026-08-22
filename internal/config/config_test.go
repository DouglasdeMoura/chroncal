package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// mustLoad calls Load and fails the test on error. Use it in tests that
// exercise valid/absent config. Tests that assert a parse error call Load
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

func TestLoad_SMTPPasswordCmdFromFile(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "chroncal")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[smtp]
host = "smtp.example.com"
password_cmd = "pass show smtp/app-password"
`), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CHRONCAL_SMTP_HOST", "")
	t.Setenv("CHRONCAL_SMTP_PASSWORD", "")
	t.Setenv("CHRONCAL_SMTP_PASSWORD_CMD", "")

	cfg := mustLoad(t)
	if cfg.SMTP.PasswordCommand != "pass show smtp/app-password" {
		t.Errorf("SMTP.PasswordCommand = %q, want %q", cfg.SMTP.PasswordCommand, "pass show smtp/app-password")
	}
}

func TestLoad_SMTPPasswordCmdFromEnv(t *testing.T) {
	t.Setenv("CHRONCAL_SMTP_PASSWORD_CMD", "secret-tool lookup smtp host")

	cfg := mustLoad(t)
	if cfg.SMTP.PasswordCommand != "secret-tool lookup smtp host" {
		t.Errorf("SMTP.PasswordCommand = %q, want %q", cfg.SMTP.PasswordCommand, "secret-tool lookup smtp host")
	}
}

// TestLoad_SMTPPasswordAndPasswordCmdBothSetIsError checks that Load rejects
// a config with both password sources. The error must surface at load time,
// not at send time when an alarm fires.
func TestLoad_SMTPPasswordAndPasswordCmdBothSetIsError(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "chroncal")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`
[smtp]
password = "literal-secret"
password_cmd = "pass show smtp/app-password"
`), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CHRONCAL_SMTP_PASSWORD", "")
	t.Setenv("CHRONCAL_SMTP_PASSWORD_CMD", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want an error when both password sources are set")
	}
	if !errors.Is(err, errSMTPPasswordConflict) {
		t.Errorf("Load() error = %v, want the mutual-exclusion error", err)
	}
}

func TestSMTPConfig_ResolvePassword_Literal(t *testing.T) {
	cfg := SMTPConfig{Password: "secret123"}
	got, err := cfg.ResolvePassword()
	if err != nil {
		t.Fatalf("ResolvePassword() error = %v", err)
	}
	if got != "secret123" {
		t.Errorf("ResolvePassword() = %q, want %q", got, "secret123")
	}
}

func TestSMTPConfig_ResolvePassword_CommandTrimsTrailingNewline(t *testing.T) {
	command := "printf 'from-cmd\\n'"
	if runtime.GOOS == "windows" {
		command = "echo from-cmd"
	}
	cfg := SMTPConfig{PasswordCommand: command}
	got, err := cfg.ResolvePassword()
	if err != nil {
		t.Fatalf("ResolvePassword() error = %v", err)
	}
	if got != "from-cmd" {
		t.Errorf("ResolvePassword() = %q, want %q (trailing newline must be trimmed)", got, "from-cmd")
	}
}

func TestSMTPConfig_ResolvePassword_CommandError(t *testing.T) {
	command := "exit 3"
	if runtime.GOOS == "windows" {
		command = "exit /b 3"
	}
	cfg := SMTPConfig{PasswordCommand: command}
	if _, err := cfg.ResolvePassword(); err == nil {
		t.Fatal("ResolvePassword() error = nil, want an error for a failing password command")
	}
}

func TestSMTPConfig_ResolvePassword_MissingCommand(t *testing.T) {
	cfg := SMTPConfig{PasswordCommand: "chroncal-definitely-not-a-real-binary-1234"}
	if _, err := cfg.ResolvePassword(); err == nil {
		t.Fatal("ResolvePassword() error = nil, want an error for a missing password command")
	}
}

func TestSMTPConfig_ResolvePassword_BothSetIsError(t *testing.T) {
	cfg := SMTPConfig{Password: "literal", PasswordCommand: "echo from-cmd"}
	if _, err := cfg.ResolvePassword(); err == nil {
		t.Fatal("ResolvePassword() error = nil, want an error when both password sources are set")
	}
}

// TestRunPasswordCommandTimeout checks that a helper which blocks cannot
// hold the alarm email forever. The test shortens passwordCmdTimeout, so it
// stays fast and deterministic.
func TestRunPasswordCommandTimeout(t *testing.T) {
	oldTimeout := passwordCmdTimeout
	passwordCmdTimeout = 200 * time.Millisecond
	t.Cleanup(func() { passwordCmdTimeout = oldTimeout })

	command := "sleep 60"
	if runtime.GOOS == "windows" {
		command = "ping -n 60 127.0.0.1"
	}

	start := time.Now()
	_, err := runPasswordCommand(command)
	if err == nil {
		t.Fatal("runPasswordCommand error = nil, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("runPasswordCommand took %v, want a return before the command ends", elapsed)
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

// TestLoad_MalformedFileReturnsError guards against a swallow of a
// syntax error in config.toml, in silence. A broken file must surface an error
// rather than be treated like an absent file. That would revert db/security/etc.
// to defaults. It would risk an open of the wrong (default) database.
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
// optional (no error). That distinguishes "no file" from "broken file".
func TestLoad_NoFileNoError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config file present
	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v, want nil when config file is absent", err)
	}
}

func TestLoad_UIWeekStartFromFile(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "chroncal")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[ui]\nweek_start = \"monday\"\n"), 0o644)

	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("CHRONCAL_UI_WEEK_START", "")

	cfg := mustLoad(t)
	if cfg.UI.WeekStart != "monday" {
		t.Errorf("UI.WeekStart = %q, want monday", cfg.UI.WeekStart)
	}
}

func TestLoad_UIWeekStartFromEnv(t *testing.T) {
	t.Setenv("CHRONCAL_UI_WEEK_START", "monday")
	cfg := mustLoad(t)
	if cfg.UI.WeekStart != "monday" {
		t.Errorf("UI.WeekStart = %q, want monday from env", cfg.UI.WeekStart)
	}
}
