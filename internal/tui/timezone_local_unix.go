//go:build !windows

package tui

import (
	"os"
	"strings"
)

// LocalIANATimezone returns the system's IANA timezone name (e.g.
// "America/Sao_Paulo"). It checks TZ, then /etc/localtime, falling back
// to "UTC".
func LocalIANATimezone() string {
	if tz := os.Getenv("TZ"); tz != "" && tz != "Local" {
		return tz
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			return target[i+len("zoneinfo/"):]
		}
	}
	return "UTC"
}
