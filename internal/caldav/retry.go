package caldav

import (
	"context"

	"github.com/douglasdemoura/chroncal/internal/retry"
)

// RetryOptions is an alias for retry.RetryOptions so existing callers compile unchanged.
type RetryOptions = retry.RetryOptions

// Retry delegates to retry.Retry.
func Retry[T any](ctx context.Context, opts RetryOptions, fn func(context.Context) (T, error)) (T, error) {
	return retry.Retry(ctx, opts, fn)
}
