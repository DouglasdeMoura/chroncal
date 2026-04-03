package caldav

import (
	"context"
	"errors"
	"testing"
)

func TestRetryRetriesTransientErrors(t *testing.T) {
	t.Parallel()

	attempts := 0
	got, err := Retry(context.Background(), RetryOptions{
		MaxAttempts: 3,
	}, func(ctx context.Context) (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("503 Service Unavailable")
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if got != "ok" {
		t.Fatalf("Retry result = %q, want ok", got)
	}
	if attempts != 3 {
		t.Fatalf("Retry attempts = %d, want 3", attempts)
	}
}

func TestRetryStopsOnNonTransientError(t *testing.T) {
	t.Parallel()

	attempts := 0
	wantErr := errors.New("400 Bad Request")
	_, err := Retry(context.Background(), RetryOptions{
		MaxAttempts: 3,
	}, func(ctx context.Context) (string, error) {
		attempts++
		return "", wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Retry error = %v, want %v", err, wantErr)
	}
	if attempts != 1 {
		t.Fatalf("Retry attempts = %d, want 1", attempts)
	}
}
