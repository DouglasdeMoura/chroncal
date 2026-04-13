package retry

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
)

var httpStatusPattern = regexp.MustCompile(`\b([1-5][0-9][0-9])\b`)

// IsTransient reports whether err is worth retrying.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	switch statusCode(err) {
	case 408, 425, 429:
		return true
	}
	if code := statusCode(err); code >= 500 {
		return true
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "server misbehaving") ||
		strings.Contains(msg, "timeout")
}

// IsConflict reports whether err represents a sync conflict.
func IsConflict(err error) bool {
	switch statusCode(err) {
	case 409, 412:
		return true
	default:
		return false
	}
}

func statusCode(err error) int {
	if err == nil {
		return 0
	}

	match := httpStatusPattern.FindStringSubmatch(err.Error())
	if len(match) != 2 {
		return 0
	}

	code := 0
	for _, r := range match[1] {
		code = (code * 10) + int(r-'0')
	}
	return code
}
