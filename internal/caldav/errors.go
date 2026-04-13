package caldav

import "github.com/douglasdemoura/chroncal/internal/retry"

// IsTransient delegates to retry.IsTransient.
func IsTransient(err error) bool { return retry.IsTransient(err) }

// IsConflict delegates to retry.IsConflict.
func IsConflict(err error) bool { return retry.IsConflict(err) }
