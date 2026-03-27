package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/BurntSushi/toml"
)

// SMTPConfig holds SMTP connection settings for EMAIL action alarms.
type SMTPConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	From     string `toml:"from"`
}

type Config struct {
	DB   string     `toml:"db"`
	SMTP SMTPConfig `toml:"smtp"`
}

// Load reads configuration with precedence: env > config file > defaults.
// The caller is responsible for applying flag overrides on top.
func Load() Config {
	var cfg Config

	// Load from config file (ignore errors — file is optional)
	if path, err := configFilePath(); err == nil {
		toml.DecodeFile(path, &cfg)
	}

	applyEnv(&cfg)
	return cfg
}

// LoadFile reads configuration from a specific file path, then applies env overrides.
func LoadFile(path string) Config {
	var cfg Config
	toml.DecodeFile(path, &cfg)
	applyEnv(&cfg)
	return cfg
}

// applyEnv applies environment variable overrides to the config.
func applyEnv(cfg *Config) {
	if v := os.Getenv("TCAL_DB"); v != "" {
		cfg.DB = v
	}
	if v := os.Getenv("TCAL_SMTP_HOST"); v != "" {
		cfg.SMTP.Host = v
	}
	if v := os.Getenv("TCAL_SMTP_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.SMTP.Port = port
		}
	}
	if v := os.Getenv("TCAL_SMTP_USERNAME"); v != "" {
		cfg.SMTP.Username = v
	}
	if v := os.Getenv("TCAL_SMTP_PASSWORD"); v != "" {
		cfg.SMTP.Password = v
	}
	if v := os.Getenv("TCAL_SMTP_FROM"); v != "" {
		cfg.SMTP.From = v
	}
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
