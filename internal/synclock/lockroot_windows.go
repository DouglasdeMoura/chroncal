//go:build windows

package synclock

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func accountLockRoot() (string, error) {
	base, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_CREATE)
	if err != nil {
		return "", fmt.Errorf("resolve lock directory: %w", err)
	}
	root := filepath.Join(base, "chroncal", "locks")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create lock directory: %w", err)
	}
	return root, nil
}
