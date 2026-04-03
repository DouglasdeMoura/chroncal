package caldav

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"
)

// BatchSize is the number of hrefs to include in a single calendar-multiget
// request. Prevents OOM on large calendars (10k events = 50MB+ parsed at once).
const BatchSize = 100

// GetResourcesBatched fetches resources in batches to avoid OOM on large calendars.
func (c *Client) GetResourcesBatched(ctx context.Context, calendarPath string, hrefs []string) ([]Resource, error) {
	var all []Resource
	for i := 0; i < len(hrefs); i += BatchSize {
		end := i + BatchSize
		if end > len(hrefs) {
			end = len(hrefs)
		}
		batch, err := c.GetResources(ctx, calendarPath, hrefs[i:end])
		if err != nil {
			return all, fmt.Errorf("multiget batch %d-%d: %w", i, end, err)
		}
		all = append(all, batch...)
	}
	return all, nil
}

// retryWithBackoff executes fn with exponential backoff on 429 and 5xx responses.
// Used internally by sync operations.
func retryWithBackoff(ctx context.Context, maxRetries int, fn func() (*http.Response, error)) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, err := fn()
		if err != nil {
			lastErr = err
			if attempt < maxRetries {
				wait := backoffDuration(attempt)
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			continue
		}

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)

			wait := backoffDuration(attempt)
			// Respect Retry-After header if present
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil {
					wait = time.Duration(secs) * time.Second
				}
			}

			if attempt < maxRetries {
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}
		}

		return resp, nil
	}
	return nil, fmt.Errorf("max retries exceeded: %w", lastErr)
}

func backoffDuration(attempt int) time.Duration {
	base := math.Pow(2, float64(attempt))
	return time.Duration(base) * time.Second
}
