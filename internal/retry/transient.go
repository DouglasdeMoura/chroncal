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

// The pattern accepts only an explicit status marker: a message that starts
// with the code ("503 Service Unavailable") or a named prefix ("HTTP 503",
// "status 503"). A bare \b[0-9]{3}\b scrape also matched batch indices,
// ports, and host segments, and classified unrelated failures as retryable.
var httpStatusPattern = regexp.MustCompile(`(?i)(?:^([1-5][0-9][0-9])\b)|(?:\b(?:https?|status)(?: code)?[: =]+([1-5][0-9][0-9]))`)

// TransientError marks an error as retryable. It optionally carries a
// server-requested minimum delay before the next attempt. One example is the
// value of an HTTP Retry-After header on a 429 or 503 response. A zero
// RetryAfter means the server gave no hint. Normal exponential backoff then
// applies.
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

// HTTPError carries an HTTP status code as a typed field. Transient and
// conflict classification can then rely on the real status instead of a
// scrape of the error string. String scrape is fragile. Any wrap that
// prepends a numeric token (a batch index, a host:port segment) would
// shadow the status and mis-route retries.
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

// IsTransient reports whether err is worth a retry.
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

	if IsRetryableStatus(statusCode(err)) {
		return true
	}

	// A bare "timeout" substring check is gone. Real timeouts carry the
	// typed net.Error Timeout method or context.DeadlineExceeded. A text
	// scrape also matched non-retryable failures whose message merely
	// mentioned the word.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "server misbehaving")
}

// IsRetryableStatus reports whether an HTTP status code is worth a retry:
// 408 (request timeout), 425 (too early), 429 (too many requests), or any 5xx.
// It is the single source of truth shared by IsTransient and the CalDAV client.
func IsRetryableStatus(code int) bool {
	switch code {
	case 408, 425, 429:
		return true
	}
	return code >= 500
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

	// Fall back to an explicit status marker in the message for legacy
	// string-only errors. The pattern rejects bare numeric tokens.
	match := httpStatusPattern.FindStringSubmatch(err.Error())
	var token string
	if len(match) > 1 {
		token = match[1]
		if token == "" && len(match) > 2 {
			token = match[2]
		}
	}
	if token == "" {
		return 0
	}

	code := 0
	for _, r := range token {
		code = (code * 10) + int(r-'0')
	}
	return code
}
