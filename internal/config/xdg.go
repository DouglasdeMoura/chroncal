package config

import (
	"os"
	"path/filepath"
)

// BaseDir resolves an XDG-style base directory. env is the XDG variable (for
// example XDG_STATE_HOME). linuxFallback is the spec path under $HOME for
// Linux, as relative path segments (for example ".local/state").
//
// The env variable wins on every OS, not just Linux. Many CLI tools adopt
// this so users on macOS and Windows relocate a directory with one setting.
// On Linux the fallback is the XDG spec path. Other platforms have no
// convention for that category, so they fall back to os.UserConfigDir.
func BaseDir(env string, goos string, linuxFallback ...string) (string, error) {
	if dir := os.Getenv(env); dir != "" {
		return dir, nil
	}
	if goos == "linux" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(append([]string{home}, linuxFallback...)...), nil
	}
	return os.UserConfigDir()
}
