package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/viper"
)

// SMTPConfig holds SMTP connection settings for EMAIL action alarms.
type SMTPConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	From     string `mapstructure:"from"`
}

type SyncConfig struct {
	Interval         string `mapstructure:"interval"`
	ConflictStrategy string `mapstructure:"conflict_strategy"`
}

type SecurityConfig struct {
	AllowUnsafeAlarmAudioAttach    bool `mapstructure:"allow_unsafe_alarm_audio_attach"`
	AllowUnsafeAlarmEmailAttendees bool `mapstructure:"allow_unsafe_alarm_email_attendees"`
}

// SoftDeleteConfig tunes the soft-delete retention window. Rows soft-deleted
// more than PurgeDays days ago are purged (hard-deleted, children cascaded)
// by the background purge job. Zero disables automatic purging; `chroncal
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

// DefaultSoftDeletePurgeDays is applied when SoftDelete.PurgeDays is zero or
// unset (viper Unmarshal leaves unset ints at zero).
const DefaultSoftDeletePurgeDays = 30

// Load reads configuration with precedence: env > config file > defaults.
// The caller is responsible for applying flag overrides on top.
func Load() Config {
	v := newViper()

	if dir, err := configDir(); err == nil {
		v.AddConfigPath(filepath.Join(dir, "chroncal"))
	}

	v.ReadInConfig() //nolint:errcheck // file is optional

	var cfg Config
	v.Unmarshal(&cfg) //nolint:errcheck // best-effort; zero-value Config is safe
	return cfg
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
	v.BindEnv("smtp.from")
	v.BindEnv("sync.interval")
	v.BindEnv("sync.conflict_strategy")
	v.BindEnv("security.allow_unsafe_alarm_audio_attach")
	v.BindEnv("security.allow_unsafe_alarm_email_attendees")
	v.BindEnv("soft_delete.purge_days")
	v.BindEnv("ui.theme")

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
