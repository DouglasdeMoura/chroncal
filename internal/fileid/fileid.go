// Package fileid identifies a file independently of the open path.
package fileid

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// Identity returns a stable identity for path. On supported filesystems it is
// based on the file ID. Moves and hard links retain one identity. Copies get
// a different one. Other platforms fall back to a canonical absolute path hash.
func Identity(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve file path: %w", err)
	}
	if canonical, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = canonical
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	if identity, ok, err := platformIdentity(absolute, info); err != nil {
		return "", err
	} else if ok {
		return identity, nil
	}
	pathHash := sha256.Sum256([]byte(filepath.Clean(absolute)))
	return fmt.Sprintf("path-%x", pathHash), nil
}
