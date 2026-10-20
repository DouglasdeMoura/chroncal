package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// caseInsensitivePaths is true on the platforms whose default file system
// folds case (macOS and Windows). Two spellings of one directory are then
// one tree.
var caseInsensitivePaths = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// SameResolvedTree is SameTree for two paths that ResolveTree already
// resolved. A caller that compares one path with many resolves it once.
func SameResolvedTree(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if caseInsensitivePaths {
		// A simple case fold. The file system of macOS folds with its own
		// tables, so two Unicode spellings of one directory can still pass
		// as two trees. The fold catches the ASCII case, which is the
		// common one.
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	if a == b {
		return true
	}
	return insideTree(a, b) || insideTree(b, a)
}

// insideTree reports whether path lies below dir. A root directory ends
// with the separator already, so the prefix does not add a second one.
func insideTree(path, dir string) bool {
	sep := string(filepath.Separator)
	return strings.HasPrefix(path, strings.TrimSuffix(dir, sep)+sep)
}

// ResolveTree returns the path with the symbolic links of its longest
// existing prefix resolved. A path below a linked directory that does not
// exist yet still lands in the resolved tree, so the overlap rule sees
// "~/.calendars/new" and "/mnt/sync/new" as one tree when ~/.calendars is
// a link to /mnt/sync. A path with no existing prefix, and an empty path,
// stay as they are.
func ResolveTree(path string) string {
	if path == "" {
		return ""
	}
	rest := ""
	for p := path; ; {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
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
// command line. A "~" or "~/" at the start becomes the home directory. The
// result is absolute and clean, so two spellings of one directory compare
// equal. An empty path stays empty.
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
	// The result keeps the spelling of the user and does not resolve a
	// symbolic link. A resolved path would change with the mount state of
	// a linked drive, and a stored path must stay stable. SameTree resolves
	// links when it compares two paths, and the import resolves them when
	// it reads.
	return filepath.Abs(path)
}
