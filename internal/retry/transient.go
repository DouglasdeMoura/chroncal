package retry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

var httpStatusPattern = regexp.MustCompile(`\b([1-5][0-9][0-9])\b`)

// TransientError marks an error as retryable and optionally carries a
// server-requested minimum delay before the next attempt, such as the value
// of an HTTP Retry-After header on a 429 or 503 response. A zero RetryAfter
// means the server gave no hint and normal exponential backoff applies.
type TransientError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *TransientError) Error() string {
	if e == nil || e.Err == nil {
		return "transient error"
	}
	return e.Err.Error()
}

func (e *TransientError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// retryAfter returns the server-requested minimum delay carried by err, if any.
func retryAfter(err error) time.Duration {
	var te *TransientError
	if errors.As(err, &te) && te.RetryAfter > 0 {
		return te.RetryAfter
	}
	return 0
}

// HTTPError carries an HTTP status code as a typed field so that
// transient/conflict classification can rely on the real status instead
// of scraping the error string. String scraping is fragile: any wrapping
// that prepends a numeric token (a batch index, a host:port segment)
// would shadow the status and mis-route retries.
type HTTPError struct {
	Status int
	// Err holds the underlying error (typically a formatted message with
	// the status text and a body excerpt). It is preserved for Error and
	// Unwrap so existing message-based diagnostics keep working.
	Err error
}

// NewHTTPError builds an HTTPError for the given status. The message is
// supplied by the caller so the rich, human-readable form is preserved.
func NewHTTPError(status int, err error) *HTTPError {
	return &HTTPError{Status: status, Err: err}
}

func (e *HTTPError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

func (e *HTTPError) Unwrap() error { return e.Err }

// IsTransient reports whether err is worth retrying.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var te *TransientError
	if errors.As(err, &te) {
		return true
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

	// Prefer the typed status when present: it is authoritative and
	// immune to numeric tokens injected by wrapping layers.
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status
	}

	// Fall back to scraping the message for legacy string-only errors.
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
