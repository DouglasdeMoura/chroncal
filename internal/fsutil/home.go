package fsutil

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome resolves a path that a user wrote in a config file or on the
// command line. A leading "~" or "~/" becomes the home directory. The result
// is absolute and clean, so two spellings of one directory compare equal.
// An empty path stays empty. config.validateCalendars accepts the same two
// prefixes, so keep the two in step.
func ExpandHome(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
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
