package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

// hasHomePrefix reports whether ExpandHome resolves the start of path to
// the home directory: a bare "~" or a "~/" prefix.
func hasHomePrefix(path string) bool {
	return path == "~" || strings.HasPrefix(path, "~/")
}

// IsAbsOrHome reports whether path does not depend on the working
// directory: it is absolute, or ExpandHome resolves its start to the home
// directory. A config file uses this rule, because the working directory
// of a launch is not stable.
func IsAbsOrHome(path string) bool {
	path = strings.TrimSpace(path)
	return hasHomePrefix(path) || filepath.IsAbs(path)
}

// ContractHome is the inverse of ExpandHome for display. A path inside the
// home directory starts with "~", so a long path stays short on screen. A
// path outside the home directory, and an empty path, stay as they are.
func ContractHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}

// ExpandHome resolves a path that a user wrote in a config file or on the
// command line. A leading "~" or "~/" becomes the home directory. The result
// is absolute and clean, so two spellings of one directory compare equal.
// An empty path stays empty.
func ExpandHome(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if hasHomePrefix(path) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[1:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
