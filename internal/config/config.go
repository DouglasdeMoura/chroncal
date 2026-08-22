package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// SMTPConfig holds SMTP connection settings for EMAIL action alarms.
type SMTPConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	From     string `mapstructure:"from"`
	// PasswordCommand is a shell command whose stdout is the SMTP password.
	// It is an alternative to a hardcoded Password in the config file. The
	// command runs at send time, not at load time. Set one of the two
	// fields, never both. Example: "pass show smtp/app-password".
	PasswordCommand string `mapstructure:"password_cmd"`
	// TLSMode controls how TLS is established for the SMTP connection.
	// Valid values:
	//   ""           – auto-detect: port 465 uses implicit TLS (SMTPS), all
	//                  other ports use STARTTLS via smtp.SendMail.
	//   "implicit"   – always use implicit TLS (tls.Dial before the SMTP
	//                  handshake); required for SMTPS / port 465.
	//   "starttls"   – always use STARTTLS (smtp.SendMail); explicit override
	//                  that disables the port-465 auto-detection.
	//   "none"       – skip implicit TLS; delegates to smtp.SendMail which
	//                  still negotiates STARTTLS when the server offers it.
	TLSMode string `mapstructure:"tls"`
}

// errSMTPPasswordConflict reports a config that sets both smtp.password and
// smtp.password_cmd. The source of the secret must stay unambiguous.
var errSMTPPasswordConflict = errors.New("smtp.password and smtp.password_cmd are mutually exclusive; set one of them")

// ResolvePassword returns the SMTP password for this configuration.
// A set PasswordCommand runs at call time and its stdout becomes the
// password. The output loses one trailing newline. The command runs through
// the system shell, so it accepts arguments and pipes. Setting both
// Password and PasswordCommand is an error: the source of the secret must
// stay unambiguous.
func (c SMTPConfig) ResolvePassword() (string, error) {
	switch {
	case c.Password != "" && c.PasswordCommand != "":
		return "", errSMTPPasswordConflict
	case c.PasswordCommand != "":
		return runPasswordCommand(c.PasswordCommand)
	default:
		return c.Password, nil
	}
}

// defaultPasswordCmdTimeout bounds how long smtp.password_cmd may run. A
// helper that blocks must not stop the alarm email forever. notify.Email
// runs from `alarm check` and from the installed service tick.
const defaultPasswordCmdTimeout = 30 * time.Second

// passwordCmdTimeout holds the active bound. A test sets it to a shorter
// value.
var passwordCmdTimeout = defaultPasswordCmdTimeout

// runPasswordCommand executes a password-retrieval command and returns its
// stdout. The error message omits stderr on purpose. Helper programs such as
// password managers can print secrets there.
func runPasswordCommand(command string) (string, error) {
	shell, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/c"
	}
	ctx, cancel := context.WithTimeout(context.Background(), passwordCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, flag, command)
	// A killed shell can leave a helper child that holds the output pipe.
	// WaitDelay bounds that wait, so the deadline holds even when the
	// shell forked its command instead of replacing itself.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("run smtp.password_cmd %q: %w", command, err)
	}
	password := strings.TrimSuffix(string(out), "\n")
	return strings.TrimSuffix(password, "\r"), nil
}

type SyncConfig struct {
	Interval         string `mapstructure:"interval"`
	ConflictStrategy string `mapstructure:"conflict_strategy"`
}

type SecurityConfig struct {
	AllowUnsafeAlarmAudioAttach    bool `mapstructure:"allow_unsafe_alarm_audio_attach"`
	AllowUnsafeAlarmEmailAttendees bool `mapstructure:"allow_unsafe_alarm_email_attendees"`
	// AllowPlaintext opts in to the plaintext credential-store fallback when
	// no OS keyring is available. Defaults to false: without it, credential
	// writes fail rather than silently persisting secrets in cleartext. The
	// --allow-plaintext flag also enables it.
	AllowPlaintext bool `mapstructure:"allow_plaintext"`
}

// SoftDeleteConfig tunes the soft-delete retention window. Rows soft-deleted
// more than PurgeDays days ago are purged (hard-deleted, children cascaded)
// by the background purge job. Zero disables automatic purge. `chroncal
// event purge-deleted` remains available for manual cleanup.
type SoftDeleteConfig struct {
	PurgeDays int `mapstructure:"purge_days"`
}

// UIConfig holds hand-editable TUI appearance preferences. Distinct from
// UIState (state.go), which tracks machine-written session state.
type UIConfig struct {
	// Theme is the name of a built-in theme under internal/tui/themes/
	// (e.g. "default", "system"). Empty falls back to the default.
	Theme string `mapstructure:"theme"`
	// WeekStart is the first day of the week in the TUI month view, week
	// view, and mini-calendar. Valid values: "sunday" (default), "monday".
	WeekStart string `mapstructure:"week_start"`
}

type Config struct {
	DB         string           `mapstructure:"db"`
	ProductID  string           `mapstructure:"product_id"`
	SMTP       SMTPConfig       `mapstructure:"smtp"`
	Sync       SyncConfig       `mapstructure:"sync"`
	Security   SecurityConfig   `mapstructure:"security"`
	SoftDelete SoftDeleteConfig `mapstructure:"soft_delete"`
	UI         UIConfig         `mapstructure:"ui"`
}

// DefaultSoftDeletePurgeDays is applied by Load when the purge_days key is
// genuinely unset. An explicit purge_days=0 is preserved as 0 (disabled) per
// the SoftDeleteConfig contract.
const DefaultSoftDeletePurgeDays = 30

// DefaultSMTPPort is applied when SMTP.Port is unset. It matches the
// documented default (587, submission with STARTTLS).
const DefaultSMTPPort = 587

// Load reads configuration with precedence: env > config file > defaults.
// The caller is responsible for flag overrides on top.
func Load() (Config, error) {
	v := newViper()

	if dir, err := configDir(); err == nil {
		v.AddConfigPath(filepath.Join(dir, "chroncal"))
	}

	// The config file is optional: a missing file is fine and falls back to
	// env/defaults. A *malformed* file, however, must surface an error rather
	// than be silently treated like an absent one — otherwise a typo anywhere
	// in config.toml would revert db/security/SMTP/etc. to defaults and could
	// open the wrong (default) database without warning.
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return Config{}, fmt.Errorf("read config file: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config file: %w", err)
	}
	// Reject an ambiguous password source at load time. The error would
	// otherwise surface only at send time, when an alarm fires. The check
	// in ResolvePassword stays as a second guard.
	if cfg.SMTP.Password != "" && cfg.SMTP.PasswordCommand != "" {
		return Config{}, errSMTPPasswordConflict
	}
	if cfg.SMTP.Port == 0 {
		cfg.SMTP.Port = DefaultSMTPPort
	}
	// Apply the purge-days default only when the key is genuinely unset.
	// An explicit purge_days=0 means "disable automatic purging" per the
	// documented contract, so it must survive as 0 rather than be rewritten
	// to the default. viper.IsSet distinguishes unset from an explicit 0.
	if !v.IsSet("soft_delete.purge_days") {
		cfg.SoftDelete.PurgeDays = DefaultSoftDeletePurgeDays
	}
	return cfg, nil
}

// newViper creates a pre-configured Viper instance with CHRONCAL_ env prefix
// and bindings for all known config keys.
func newViper() *viper.Viper {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("toml")
	v.SetEnvPrefix("CHRONCAL")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Bind env vars so Unmarshal picks them up even without a config file.
	v.BindEnv("db")
	v.BindEnv("product_id")
	v.BindEnv("smtp.host")
	v.BindEnv("smtp.port")
	v.BindEnv("smtp.username")
	v.BindEnv("smtp.password")
	v.BindEnv("smtp.password_cmd")
	v.BindEnv("smtp.from")
	v.BindEnv("smtp.tls")
	v.BindEnv("sync.interval")
	v.BindEnv("sync.conflict_strategy")
	v.BindEnv("security.allow_unsafe_alarm_audio_attach")
	v.BindEnv("security.allow_unsafe_alarm_email_attendees")
	v.BindEnv("security.allow_plaintext")
	v.BindEnv("soft_delete.purge_days")
	v.BindEnv("ui.theme")
	v.BindEnv("ui.week_start")

	return v
}

func configDir() (string, error) {
	// Honour XDG_CONFIG_HOME on every OS. Many CLI tools do this so users
	// on macOS/Windows can override the default config location.
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir, nil
	}
	if runtime.GOOS == "linux" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config"), nil
	}
	return os.UserConfigDir()
}
