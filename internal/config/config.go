package config

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/douglasdemoura/chroncal/internal/secretcmd"
	"github.com/spf13/viper"
)

// SMTPConfig holds SMTP connection settings for EMAIL action alarms.
type SMTPConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	// PasswordCommand holds a shell command that prints the SMTP password
	// on its first standard-output line. It keeps the password out of the
	// config file. Password and PasswordCommand are mutually exclusive.
	PasswordCommand string `mapstructure:"password_cmd"`
	From            string `mapstructure:"from"`
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

type SyncConfig struct {
	Interval         string `mapstructure:"interval"`
	ConflictStrategy string `mapstructure:"conflict_strategy"`
	// HTTPTimeout bounds one CalDAV request, as a Go duration string (for
	// example "5m"). An empty value keeps the built-in default of the caldav
	// package. Raise it for a server that answers a large multiget slowly.
	// Lower it to fail faster against a hung server.
	HTTPTimeout string `mapstructure:"http_timeout"`
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
	if err := cfg.SMTP.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ErrSMTPPasswordSourceConflict reports a config that sets both an SMTP
// password and an SMTP password command. Two sources hide which secret the
// program sends, so Load rejects the pair.
var ErrSMTPPasswordSourceConflict = errors.New(
	"smtp.password and smtp.password_cmd are mutually exclusive; set only one")

// Validate reports a conflict between the two SMTP password sources.
func (c SMTPConfig) Validate() error {
	if strings.TrimSpace(c.Password) != "" && strings.TrimSpace(c.PasswordCommand) != "" {
		return ErrSMTPPasswordSourceConflict
	}
	return nil
}

// ResolvePassword returns the SMTP password. It runs smtp.password_cmd when
// that key has a value. The caller must not log or print the result.
func (c SMTPConfig) ResolvePassword(ctx context.Context) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	command := strings.TrimSpace(c.PasswordCommand)
	if command == "" {
		return c.Password, nil
	}
	secret, err := secretcmd.Run(ctx, command)
	if err != nil {
		return "", fmt.Errorf("resolve smtp.password_cmd: %w", err)
	}
	return secret, nil
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
	// An unset conflict strategy must mean the safe mode: record the
	// conflict and wait for the user. Server-wins is an explicit opt-in.
	v.SetDefault("sync.conflict_strategy", "prompt")
	v.BindEnv("sync.interval")
	v.BindEnv("sync.conflict_strategy")
	v.BindEnv("sync.http_timeout")
	v.BindEnv("security.allow_unsafe_alarm_audio_attach")
	v.BindEnv("security.allow_unsafe_alarm_email_attendees")
	v.BindEnv("security.allow_plaintext")
	v.BindEnv("soft_delete.purge_days")
	v.BindEnv("ui.theme")
	v.BindEnv("ui.week_start")

	return v
}

func configDir() (string, error) {
	return BaseDir("XDG_CONFIG_HOME", runtime.GOOS, ".config")
}
