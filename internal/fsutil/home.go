package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// hasHomePrefix reports whether ExpandHome resolves the start of path to
// the home directory: a bare "~" or a "~/" prefix.
func hasHomePrefix(path string) bool {
	return path == "~" || strings.HasPrefix(path, "~/")
}

// ErrOtherUserHome reports the "~user/dir" form. chroncal does not look up
// another user's home directory, and a quoted value reaches the program
// with the tilde intact, so a silent resolve would name a directory that
// does not exist.
var ErrOtherUserHome = errors.New(`the "~user" form is not supported; use an absolute path or one that starts with ~/`)

// IsAbsOrHome reports whether path does not depend on the working
// directory: it is absolute, or ExpandHome resolves its start to the home
// directory. A config file uses this rule, because the working directory
// of a launch is not stable.
func IsAbsOrHome(path string) bool {
	path = strings.TrimSpace(path)
	return hasHomePrefix(path) || filepath.IsAbs(path)
}

// SameTree reports whether two clean absolute directories are one tree:
// equal, or one inside the other. An empty path is in no tree.
func SameTree(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(a, b+sep) || strings.HasPrefix(b, a+sep)
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
	// A HOME with a trailing separator must still match.
	home = filepath.Clean(home)
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}

// ExpandHome resolves a path that a user wrote in a config file or on the
// command line. A "~" or "~/" at the start becomes the home directory. The result
// is absolute, and filepath.Abs cleans it, so two spellings of one
// directory compare equal. An empty path stays empty.
func ExpandHome(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~") && !hasHomePrefix(path) {
		return "", fmt.Errorf("%s: %w", path, ErrOtherUserHome)
	}
	if hasHomePrefix(path) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[1:])
	}
	return filepath.Abs(path)
}
