package slots

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func peakInFlight(t *testing.T, files int, size int64, count int) int64 {
	t.Helper()
	limiter := New(files)
	var inFlight, peak atomic.Int64
	var wg sync.WaitGroup
	for range count {
		release, err := limiter.Acquire(context.Background(), size)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer release()
			now := inFlight.Add(1)
			for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
			}
			time.Sleep(5 * time.Millisecond)
			inFlight.Add(-1)
		}()
	}
	limiter.Wait()
	wg.Wait()
	return peak.Load()
}

func TestLargeFilesKeepTheFileCountAndSmallOnesGetFourTimesAsMany(t *testing.T) {
	if got := peakInFlight(t, 4, 8<<20, 40); got != 4 {
		t.Fatalf("large files: %d in flight, want 4", got)
	}
	if got := peakInFlight(t, 4, 2000, 80); got != 16 {
		t.Fatalf("small files: %d in flight, want 16", got)
	}
}
