//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package synclock

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// accountLockRoot is independent of XDG_CACHE_HOME and other per-process
// environment. Processes for the same OS user must resolve the same root or
// they can take different lock files for the same database inode.
func accountLockRoot() (string, error) {
	current, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		return "", fmt.Errorf("resolve current user for lock directory: %w", err)
	}
	if current.HomeDir == "" {
		return "", fmt.Errorf("resolve current user for lock directory: empty home directory")
	}
	root := filepath.Join(current.HomeDir, ".cache", "chroncal", "locks")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create lock directory: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect lock directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("unsafe lock directory %q", root)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(root, 0o700); err != nil {
			return "", fmt.Errorf("secure lock directory: %w", err)
		}
	}
	return root, nil
}
