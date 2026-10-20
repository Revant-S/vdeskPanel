package drive

import (
	"math/rand/v2"
	"time"
)

type RetryConfig struct {
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	MaxAttempts uint
}

func DefaultRetryConfig() RetryConfig {
	return RetryConfig{BaseDelay: time.Second, MaxDelay: 64 * time.Second, MaxAttempts: 8}
}

// fullJitterBackOff implements backoff.BackOff with "full jitter": each delay is uniform in
// [0, min(max, base*2^attempt)]. The library's own randomisation only jitters around the
// exponential value, which is not what the spec asks for.
type fullJitterBackOff struct {
	base    time.Duration
	max     time.Duration
	attempt uint
}

func newFullJitterBackOff(cfg RetryConfig) *fullJitterBackOff {
	return &fullJitterBackOff{base: cfg.BaseDelay, max: cfg.MaxDelay}
}

func (b *fullJitterBackOff) Reset() { b.attempt = 0 }

func (b *fullJitterBackOff) NextBackOff() time.Duration {
	ceiling := b.ceiling()
	b.attempt++
	if ceiling <= 0 {
		return 0
	}
	return rand.N(ceiling + 1)
}

func (b *fullJitterBackOff) ceiling() time.Duration {
	ceiling := b.base
	for range b.attempt {
		if ceiling >= b.max/2 {
			return b.max
		}
		ceiling *= 2
	}
	return min(ceiling, b.max)
}
