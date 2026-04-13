package retry

import (
	"math"
	"math/rand/v2"
	"time"
)

// backoffDuration returns a random duration in [0, 2^attempt) seconds
// (full jitter). This spreads retries uniformly across the backoff window,
// reducing thundering-herd collisions compared to equal or decorrelated jitter.
func backoffDuration(attempt int) time.Duration {
	ceiling := math.Pow(2, float64(attempt)) // seconds
	const maxBackoff = 60.0
	if ceiling > maxBackoff {
		ceiling = maxBackoff
	}
	jittered := rand.Float64() * ceiling // [0, ceiling)
	return time.Duration(jittered * float64(time.Second))
}
