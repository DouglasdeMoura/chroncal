package retry

import (
	"math"
	"math/rand/v2"
	"time"
)

// maxBackoffDelay is the maximum sleep between two retries.
// backoffDuration derives its ceiling from it. retryAfterDelay derives its
// Retry-After cap from it. One constant keeps both waits in step.
const maxBackoffDelay = 60 * time.Second

// backoffDuration returns a random duration in [0, 2^attempt) seconds
// (full jitter). This spreads retries uniformly across the backoff window.
// It reduces thundering-herd collisions compared to equal or decorrelated jitter.
func backoffDuration(attempt int) time.Duration {
	maxBackoff := maxBackoffDelay.Seconds()
	ceiling := math.Pow(2, float64(attempt)) // seconds
	if ceiling > maxBackoff {
		ceiling = maxBackoff
	}
	jittered := rand.Float64() * ceiling // [0, ceiling)
	return time.Duration(jittered * float64(time.Second))
}
