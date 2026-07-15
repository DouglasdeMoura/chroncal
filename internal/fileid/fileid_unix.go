//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package fileid

import (
	"fmt"
	"os"
	"syscall"
)

func platformIdentity(_ string, info os.FileInfo) (string, bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false, nil
	}
	return fmt.Sprintf("inode-%x-%x", stat.Dev, stat.Ino), true, nil
}
