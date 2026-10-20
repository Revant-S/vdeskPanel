package drive

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type BudgetConfig struct {
	MetadataWritesPerSecond float64
	MetadataWriteBurst      int
	ReadsPerSecond          float64
	ReadBurst               int
}

func DefaultBudgetConfig() BudgetConfig {
	return BudgetConfig{
		MetadataWritesPerSecond: 3,
		MetadataWriteBurst:      10,
		ReadsPerSecond:          10,
		ReadBurst:               30,
	}
}

const (
	recoveryInterval = time.Minute
	recoveryFactor   = 1.1
	// A bucket never drops below this fraction of its ceiling, so a storm of concurrent
	// rate-limit answers cannot stall the app for minutes per request.
	minimumRateFraction = 1.0 / 16
)

// adaptiveBucket halves its rate on a rate-limit answer and regains 10% of its current
// rate per error-free minute, up to the configured ceiling.
type adaptiveBucket struct {
	mu          sync.Mutex
	limiter     *rate.Limiter
	ceiling     rate.Limit
	floor       rate.Limit
	lastEventAt time.Time
	now         func() time.Time
}

func newAdaptiveBucket(perSecond float64, burst int, now func() time.Time) *adaptiveBucket {
	ceiling := rate.Limit(perSecond)
	return &adaptiveBucket{
		limiter:     rate.NewLimiter(ceiling, burst),
		ceiling:     ceiling,
		floor:       ceiling * minimumRateFraction,
		lastEventAt: now(),
		now:         now,
	}
}

func (b *adaptiveBucket) wait(ctx context.Context) error {
	b.recover()
	if err := b.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("wait for request budget: %w", err)
	}
	return nil
}

func (b *adaptiveBucket) penalize() {
	b.mu.Lock()
	defer b.mu.Unlock()
	halved := max(b.limiter.Limit()/2, b.floor)
	b.limiter.SetLimit(halved)
	b.lastEventAt = b.now()
}

func (b *adaptiveBucket) recover() {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	current := b.limiter.Limit()
	for current < b.ceiling && now.Sub(b.lastEventAt) >= recoveryInterval {
		b.lastEventAt = b.lastEventAt.Add(recoveryInterval)
		current = min(current*recoveryFactor, b.ceiling)
	}
	if current >= b.ceiling {
		b.lastEventAt = now
	}
	b.limiter.SetLimit(current)
}

func (b *adaptiveBucket) currentRate() float64 {
	b.recover()
	return float64(b.limiter.Limit())
}

// Budget holds the two request buckets shared by every call of one Client.
type Budget struct {
	metadataWrites *adaptiveBucket
	reads          *adaptiveBucket
}

func newBudget(cfg BudgetConfig, now func() time.Time) *Budget {
	return &Budget{
		metadataWrites: newAdaptiveBucket(cfg.MetadataWritesPerSecond, cfg.MetadataWriteBurst, now),
		reads:          newAdaptiveBucket(cfg.ReadsPerSecond, cfg.ReadBurst, now),
	}
}

func (b *Budget) MetadataWriteRate() float64 { return b.metadataWrites.currentRate() }

func (b *Budget) ReadRate() float64 { return b.reads.currentRate() }
