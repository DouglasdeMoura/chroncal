//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package fileid

import "os"

func platformIdentity(_ string, _ os.FileInfo) (string, bool, error) {
	return "", false, nil
}
