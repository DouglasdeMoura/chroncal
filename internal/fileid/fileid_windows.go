//go:build windows

package fileid

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func platformIdentity(path string, _ os.FileInfo) (string, bool, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false, fmt.Errorf("encode file path: %w", err)
	}
	handle, err := windows.CreateFile(
		path16,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return "", false, fmt.Errorf("open file identity handle: %w", err)
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return "", false, fmt.Errorf("read file identity: %w", err)
	}
	return fmt.Sprintf("file-%x-%x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), true, nil
}
