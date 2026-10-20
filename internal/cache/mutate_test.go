package cache

import (
	"context"
	"errors"
	"testing"

	"github.com/cosmoboy/vault/internal/model"
)

func TestWriteOnIncompleteBodyNeedsEnsureComplete(t *testing.T) {
	rig := newTestRig(t)
	plaintext := patternedPlaintext(elevenSegments)
	bodyID := rig.publishRemoteBody(t, testID, plaintext)
	c := rig.newCache(t, 4)
	ctx := context.Background()
	entry, err := c.OpenWritable(ctx, testID, int64(len(plaintext)), model.CloudOnly, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	defer entry.Close()
	if _, err := entry.WriteAt(ctx, []byte("x"), 5*segment); !errors.Is(err, ErrIncompleteBody) {
		t.Fatalf("WriteAt on incomplete: %v", err)
	}
	if err := entry.Truncate(ctx, 10); !errors.Is(err, ErrIncompleteBody) {
		t.Fatalf("Truncate on incomplete: %v", err)
	}
	if err := entry.EnsureComplete(ctx); err != nil {
		t.Fatal(err)
	}
	if !entry.Complete() {
		t.Fatal("not complete after EnsureComplete")
	}
	if _, err := entry.WriteAt(ctx, []byte("x"), 5*segment); err != nil {
		t.Fatal(err)
	}
	plaintext[5*segment] = 'x'
	assertRange(t, entry, plaintext, 5*segment-10, 20)
	if rig.bitmaps.has(testID) {
		t.Fatal("bitmap record kept after the body became local-authored")
	}
	if err := entry.Truncate(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if entry.Size() != 3 {
		t.Fatalf("Size = %d", entry.Size())
	}
}

func TestTruncateToZeroOnIncompleteStartsNewBody(t *testing.T) {
	rig := newTestRig(t)
	plaintext := patternedPlaintext(elevenSegments)
	bodyID := rig.publishRemoteBody(t, testID, plaintext)
	c := rig.newCache(t, 4)
	ctx := context.Background()
	entry, err := c.OpenWritable(ctx, testID, int64(len(plaintext)), model.Partial, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	defer entry.Close()
	if err := entry.Truncate(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if entry.Size() != 0 || !entry.Complete() {
		t.Fatalf("after truncate 0: size %d complete %v", entry.Size(), entry.Complete())
	}
	if _, err := entry.WriteAt(ctx, []byte("fresh"), 0); err != nil {
		t.Fatal(err)
	}
	if got := readRange(t, entry, 0, 10); string(got) != "fresh" {
		t.Fatalf("read %q", got)
	}
	assertFetched(t, rig.remote)
}

func TestReadOnlyEntryRefusesWrites(t *testing.T) {
	rig := newTestRig(t)
	c := rig.newCache(t, 4)
	ctx := context.Background()
	created, err := c.Create(ctx, testID)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := c.Open(ctx, testID, 0, model.Local, [16]byte{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.WriteAt(ctx, []byte("x"), 0); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("WriteAt on reader: %v", err)
	}
	if _, err := created.WriteAt(ctx, []byte("shared"), 0); err != nil {
		t.Fatal(err)
	}
	if reader.Size() != 6 || string(readRange(t, reader, 0, 6)) != "shared" {
		t.Fatal("second handle does not see the shared body")
	}
	created.Close()
	if _, err := created.WriteAt(ctx, []byte("x"), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteAt after close: %v", err)
	}
	if err := reader.Sync(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalBodySizeComesFromFile(t *testing.T) {
	rig := newTestRig(t)
	c := rig.newCache(t, 4)
	ctx := context.Background()
	created, err := c.Create(ctx, testID)
	if err != nil {
		t.Fatal(err)
	}
	created.WriteAt(ctx, patternedPlaintext(70000), 0)
	created.Close()
	reopened, err := c.Open(ctx, testID, 12, model.Dirty, [16]byte{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Size() != 70000 {
		t.Fatalf("Size = %d, want the file's 70000", reopened.Size())
	}
	if _, err := c.Open(ctx, "ffeeddccbbaa99887766554433221100", 0, model.Local, [16]byte{}); !errors.Is(err, ErrNoBody) {
		t.Fatalf("open missing local body: %v", err)
	}
}
