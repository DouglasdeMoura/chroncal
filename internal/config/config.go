package config

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/BurntSushi/toml"
)

type Config struct {
	DB string `toml:"db"`
}

// Load reads configuration with precedence: env > config file > defaults.
// The caller is responsible for applying flag overrides on top.
func Load() Config {
	var cfg Config

	// Load from config file (ignore errors — file is optional)
	if path, err := configFilePath(); err == nil {
		toml.DecodeFile(path, &cfg)
	}

	// Environment variables override config file
	if v := os.Getenv("TCAL_DB"); v != "" {
		cfg.DB = v
	}

	return cfg
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
