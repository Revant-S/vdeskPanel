package trash

import (
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/model"
)

type threadsOf map[uint32]uint32

func (t threadsOf) ProcessOf(thread uint32) (uint32, error) { return t[thread], nil }

func TestWholeReadByAnyThreadOfTheDeleterIsAMoveOut(t *testing.T) {
	now := time.Unix(100, 0)
	detector := NewMoveOutDetector(threadsOf{11: 10, 12: 10, 21: 20}, 10*time.Second, func() time.Time { return now })
	detector.ObserveRead("a", 11, 65536, 34464, 100000)
	detector.ObserveRead("a", 11, 0, 65536, 100000)
	if got := detector.ClassifyDelete("a", 12); got != model.ReasonMovedOut {
		t.Fatalf("worker thread of the reading process: %s", got)
	}
	if got := detector.ClassifyDelete("a", 21); got != model.ReasonDeleted {
		t.Fatalf("another process: %s", got)
	}
	detector.ObserveRead("b", 11, 0, 500, 1000)
	if got := detector.ClassifyDelete("b", 11); got != model.ReasonDeleted {
		t.Fatalf("partial read: %s", got)
	}
	detector.ObserveCachedOpen("c", 21)
	now = now.Add(11 * time.Second)
	if got := detector.ClassifyDelete("c", 21); got != model.ReasonDeleted {
		t.Fatalf("read outside the window: %s", got)
	}
}
