package cache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/vaultfile"
)

const segment = vaultfile.SegmentPlaintextSize

// elevenSegments spans runs 0-3, 4-7 and 8-10 with a four-segment run size.
var elevenSegments = 10*segment + 100

func TestSparseReadFetchesAlignedRunsOnce(t *testing.T) {
	rig := newTestRig(t)
	plaintext := patternedPlaintext(elevenSegments)
	bodyID := rig.publishRemoteBody(t, testID, plaintext)
	c := rig.newCache(t, 4)
	entry, err := c.Open(context.Background(), testID, int64(len(plaintext)), model.CloudOnly, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	defer entry.Close()
	size := int64(len(plaintext))

	assertRange(t, entry, plaintext, 5*segment+10, 100)
	assertFetched(t, rig.remote, headerRange, segmentRange(4, 7, size))
	assertRange(t, entry, plaintext, 6*segment, 500)
	assertFetched(t, rig.remote)
	assertRange(t, entry, plaintext, 9*segment+3, segment)
	assertFetched(t, rig.remote, segmentRange(8, 10, size))
	if entry.Complete() {
		t.Fatal("complete with run 0 missing")
	}
	assertRange(t, entry, plaintext, 3*segment-5, 2*segment)
	assertFetched(t, rig.remote, segmentRange(0, 3, size))
	if !entry.Complete() {
		t.Fatal("not complete after every run was read")
	}
}

func assertFetched(t *testing.T, remote *fakeRemote, want ...fetchedRange) {
	t.Helper()
	got := remote.takeFetched()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("fetched %v, want %v", got, want)
	}
}

func TestSequentialReadPrefetchesNextRun(t *testing.T) {
	rig := newTestRig(t)
	plaintext := patternedPlaintext(elevenSegments)
	bodyID := rig.publishRemoteBody(t, testID, plaintext)
	c := rig.newCache(t, 4)
	entry, err := c.Open(context.Background(), testID, int64(len(plaintext)), model.CloudOnly, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	defer entry.Close()
	assertRange(t, entry, plaintext, 0, 4096)
	rig.remote.waitForFetch(t, segmentRange(4, 7, int64(len(plaintext))))
	assertRange(t, entry, plaintext, 4096, 5*segment)
}

func TestBitmapPersistsAcrossReopen(t *testing.T) {
	rig := newTestRig(t)
	plaintext := patternedPlaintext(elevenSegments)
	size := int64(len(plaintext))
	bodyID := rig.publishRemoteBody(t, testID, plaintext)
	first := rig.newCache(t, 4)
	entry, err := first.Open(context.Background(), testID, size, model.CloudOnly, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	assertRange(t, entry, plaintext, 5*segment, 10)
	entry.Close()
	first.Close()
	rig.remote.takeFetched()

	second := rig.newCache(t, 4)
	reopened, err := second.Open(context.Background(), testID, size, model.Partial, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertRange(t, reopened, plaintext, 4*segment, 4*segment)
	assertFetched(t, rig.remote)
}

func TestHeaderBodyIDMismatchIsTampered(t *testing.T) {
	rig := newTestRig(t)
	plaintext := patternedPlaintext(3 * segment)
	rig.publishRemoteBody(t, testID, plaintext)
	c := rig.newCache(t, 4)
	entry, err := c.Open(context.Background(), testID, int64(len(plaintext)), model.CloudOnly, vaultfile.BodyID{1})
	if err != nil {
		t.Fatal(err)
	}
	defer entry.Close()
	_, err = entry.ReadAt(context.Background(), make([]byte, 10), 0)
	if !errors.Is(err, ErrBodyMismatch) || !errors.Is(err, vaultfile.ErrTampered) {
		t.Fatalf("err = %v", err)
	}
	assertFetched(t, rig.remote, headerRange)
}

func flipCacheByte(t *testing.T, c *Cache, id model.ObjectID, offset int64) {
	t.Helper()
	file, err := os.OpenFile(c.BodyPath(id), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	one := make([]byte, 1)
	if _, err := file.ReadAt(one, offset); err != nil {
		t.Fatal(err)
	}
	one[0] ^= 0xff
	if _, err := file.WriteAt(one, offset); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptSegmentOfMirrorIsRefetched(t *testing.T) {
	rig := newTestRig(t)
	plaintext := patternedPlaintext(elevenSegments)
	size := int64(len(plaintext))
	bodyID := rig.publishRemoteBody(t, testID, plaintext)
	c := rig.newCache(t, 4)
	entry, err := c.Open(context.Background(), testID, size, model.CloudOnly, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	assertRange(t, entry, plaintext, 5*segment, 10)
	entry.Close()
	rig.remote.takeFetched()
	flipCacheByte(t, c, testID, vaultfile.SegmentOffset(5)+40)

	reopened, err := c.Open(context.Background(), testID, size, model.Partial, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertRange(t, reopened, plaintext, 5*segment, 10)
	assertFetched(t, rig.remote, segmentRange(5, 5, size))
}

func TestCorruptSegmentOfDirtyBodyIsReported(t *testing.T) {
	rig := newTestRig(t)
	c := rig.newCache(t, 4)
	entry, err := c.Create(context.Background(), testID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := patternedPlaintext(3 * segment)
	if _, err := entry.WriteAt(context.Background(), plaintext, 0); err != nil {
		t.Fatal(err)
	}
	entry.Close()
	flipCacheByte(t, c, testID, vaultfile.SegmentOffset(1)+40)

	reopened, err := c.Open(context.Background(), testID, int64(len(plaintext)), model.Dirty, vaultfile.BodyID{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, err = reopened.ReadAt(context.Background(), make([]byte, 10), segment+5)
	var segmentErr *vaultfile.SegmentError
	if !errors.Is(err, ErrCorruptBody) || !errors.As(err, &segmentErr) || segmentErr.Index != 1 {
		t.Fatalf("err = %v", err)
	}
	assertFetched(t, rig.remote)
}

func TestConcurrentSparseReads(t *testing.T) {
	rig := newTestRig(t)
	plaintext := patternedPlaintext(elevenSegments)
	bodyID := rig.publishRemoteBody(t, testID, plaintext)
	c := rig.newCache(t, 2)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry, err := c.Open(context.Background(), testID, int64(len(plaintext)), model.CloudOnly, bodyID)
			if err != nil {
				errs <- err
				return
			}
			defer entry.Close()
			for step := range 20 {
				offset := int64((worker*131 + step*977) % len(plaintext))
				buf := make([]byte, 3000)
				n, _ := entry.ReadAt(context.Background(), buf, offset)
				if string(buf[:n]) != string(plaintext[offset:offset+int64(n)]) {
					errs <- fmt.Errorf("worker %d: bytes at %d differ", worker, offset)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestStaleSparseBodyIsDroppedWhenRemoteChanges(t *testing.T) {
	rig := newTestRig(t)
	c := rig.newCache(t, 4)
	ctx := context.Background()
	before := patternedPlaintext(3 * segment)
	beforeID := rig.publishRemoteBody(t, testID, before)
	entry, err := c.Open(ctx, testID, int64(len(before)), model.CloudOnly, beforeID)
	if err != nil {
		t.Fatal(err)
	}
	assertRange(t, entry, before, 0, 10)
	entry.Close()

	after := []byte("a different remote version")
	afterID := rig.publishRemoteBody(t, testID, after)
	rig.remote.takeFetched()
	reopened, err := c.Open(ctx, testID, int64(len(after)), model.Partial, afterID)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertRange(t, reopened, after, 0, int64(len(after)))
	assertFetched(t, rig.remote, headerRange, segmentRange(0, 0, int64(len(after))))
}
