package caldav

import (
	"context"
	"fmt"
	"time"
)

// RetryOptions controls retry behavior for transient CalDAV/WebDAV operations.
type RetryOptions struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// Retry reruns fn when it fails with a transient error.
func Retry[T any](ctx context.Context, opts RetryOptions, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	opts = opts.withDefaults()

	var lastErr error
	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		value, err := fn(ctx)
		if err == nil {
			return value, nil
		}

		lastErr = err
		if !IsTransient(err) || attempt == opts.MaxAttempts {
			return zero, err
		}

		delay := retryDelay(opts, attempt-1)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return zero, fmt.Errorf("retry canceled: %w", ctx.Err())
		}
	}

	return zero, lastErr
}

func (o RetryOptions) withDefaults() RetryOptions {
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 3
	}
	if o.BaseDelay <= 0 {
		o.BaseDelay = 200 * time.Millisecond
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = 2 * time.Second
	}
	return o
}

func retryDelay(opts RetryOptions, attempt int) time.Duration {
	if opts.BaseDelay <= 0 || opts.MaxDelay <= 0 {
		return 0
	}

	wait := backoffDuration(attempt)
	if wait < opts.BaseDelay {
		return opts.BaseDelay
	}
	if wait > opts.MaxDelay {
		return opts.MaxDelay
	}
	return wait
}
