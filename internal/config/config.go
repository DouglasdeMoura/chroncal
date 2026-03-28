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

type Config struct {
	DB   string     `mapstructure:"db"`
	SMTP SMTPConfig `mapstructure:"smtp"`
}

// Load reads configuration with precedence: env > config file > defaults.
// The caller is responsible for applying flag overrides on top.
func Load() Config {
	v := newViper()

	if dir, err := configDir(); err == nil {
		v.AddConfigPath(filepath.Join(dir, "tcal"))
	}

	v.ReadInConfig() // ignore error — file is optional

	var cfg Config
	v.Unmarshal(&cfg)
	return cfg
}

// LoadFile reads configuration from a specific file path, then applies env overrides.
func LoadFile(path string) Config {
	v := newViper()
	v.SetConfigFile(path)
	v.ReadInConfig()

	var cfg Config
	v.Unmarshal(&cfg)
	return cfg
}

// newViper creates a pre-configured Viper instance with TCAL_ env prefix
// and bindings for all known config keys.
func newViper() *viper.Viper {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("toml")
	v.SetEnvPrefix("TCAL")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Bind env vars so Unmarshal picks them up even without a config file.
	v.BindEnv("db")
	v.BindEnv("smtp.host")
	v.BindEnv("smtp.port")
	v.BindEnv("smtp.username")
	v.BindEnv("smtp.password")
	v.BindEnv("smtp.from")

	return v
}

// configFilePath returns the OS-appropriate config file location.
// Linux: $XDG_CONFIG_HOME/tcal/config.toml
// macOS/Windows: os.UserConfigDir()/tcal/config.toml
func configFilePath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tcal", "config.toml"), nil
}

func configDir() (string, error) {
	if runtime.GOOS == "linux" {
		if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
			return dir, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config"), nil
	}
	return os.UserConfigDir()
}
