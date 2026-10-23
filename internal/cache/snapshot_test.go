package cache

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vault/internal/model"
	"vault/internal/plaintextcheck"
	"vault/internal/vaultfile"
)

const otherID model.ObjectID = "fedcba9876543210fedcba9876543210"

func createWithContent(t *testing.T, c *Cache, id model.ObjectID, plaintext []byte) *Entry {
	t.Helper()
	entry, err := c.Create(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.WriteAt(context.Background(), plaintext, 0); err != nil {
		t.Fatal(err)
	}
	return entry
}

func readSnapshotPlaintext(t *testing.T, rig *testRig, snapshot SnapshotFile) []byte {
	t.Helper()
	file, err := os.Open(snapshot.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := vaultfile.OpenBody(file, rig.content, snapshot.PlaintextSize, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	out := make([]byte, snapshot.PlaintextSize)
	if _, err := body.ReadAt(out, 0); err != nil && len(out) > 0 {
		t.Fatal(err)
	}
	return out
}

func TestSnapshotIsIsolatedFromLaterWrites(t *testing.T) {
	rig := newTestRig(t)
	c := rig.newCache(t, 4)
	original := patternedPlaintext(2*segment + 9)
	entry := createWithContent(t, c, testID, original)
	defer entry.Close()

	snapshot, err := c.Snapshot(context.Background(), "batch1", testID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.WriteAt(context.Background(), []byte("later"), 0); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := os.ReadFile(snapshot.Path)
	if err != nil {
		t.Fatal(err)
	}
	if md5.Sum(ciphertext) != snapshot.CiphertextMD5 || int64(len(ciphertext)) != snapshot.CiphertextSize {
		t.Fatal("snapshot MD5 or size does not match the file")
	}
	if snapshot.PlaintextSHA256 != sha256.Sum256(original) {
		t.Fatal("snapshot SHA-256 does not match the original plaintext")
	}
	if !bytes.Equal(readSnapshotPlaintext(t, rig, snapshot), original) {
		t.Fatal("snapshot content changed after a later write")
	}
	live, err := vaultfile.ReadHeader(mustOpen(t, c.BodyPath(testID)))
	if err != nil || live.BodyID == snapshot.Header.BodyID {
		t.Fatalf("snapshot reuses the live BodyID (err %v)", err)
	}
	if want := filepath.Join(rig.dir, "snapshots", "batch1", string(testID)); snapshot.Path != want {
		t.Fatalf("Path = %s, want %s", snapshot.Path, want)
	}
	if err := c.RemoveSnapshot("batch1", testID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(snapshot.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty batch dir kept: %v", err)
	}
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func TestSnapshotOfIncompleteBodyIsRefused(t *testing.T) {
	rig := newTestRig(t)
	plaintext := patternedPlaintext(elevenSegments)
	bodyID := rig.publishRemoteBody(t, testID, plaintext)
	c := rig.newCache(t, 4)
	entry, err := c.Open(context.Background(), testID, int64(len(plaintext)), model.CloudOnly, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	assertRange(t, entry, plaintext, 0, 10)
	if _, err := c.Snapshot(context.Background(), "b", testID); !errors.Is(err, ErrIncompleteBody) {
		t.Fatalf("snapshot of open sparse body: %v", err)
	}
	entry.Close()
	if _, err := c.Snapshot(context.Background(), "b", testID); !errors.Is(err, ErrIncompleteBody) {
		t.Fatalf("snapshot of closed sparse body: %v", err)
	}
}

func TestSnapshotOfClosedLocalBody(t *testing.T) {
	rig := newTestRig(t)
	c := rig.newCache(t, 4)
	plaintext := patternedPlaintext(segment)
	createWithContent(t, c, testID, plaintext).Close()
	snapshot, err := c.Snapshot(context.Background(), "b", testID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readSnapshotPlaintext(t, rig, snapshot), plaintext) {
		t.Fatal("snapshot content differs")
	}
}

func TestReplaceKeepsOldReaderWorking(t *testing.T) {
	rig := newTestRig(t)
	c := rig.newCache(t, 4)
	ctx := context.Background()
	oldContent, newContent := []byte("old body content"), []byte("new body, longer content")
	createWithContent(t, c, testID, oldContent).Close()
	reader, err := c.Open(ctx, testID, 0, model.Local, vaultfile.BodyID{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	createWithContent(t, c, otherID, newContent).Close()
	snapshot, err := c.Snapshot(ctx, "b", otherID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Replace(ctx, testID, snapshot.Path); err != nil {
		t.Fatal(err)
	}
	if got := readRange(t, reader, 0, 100); !bytes.Equal(got, oldContent) {
		t.Fatalf("old reader sees %q", got)
	}
	fresh, err := c.Open(ctx, testID, 0, model.Local, vaultfile.BodyID{})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if got := readRange(t, fresh, 0, 100); !bytes.Equal(got, newContent) {
		t.Fatalf("new reader sees %q", got)
	}
}

func TestFreeRefusesOpenWriter(t *testing.T) {
	rig := newTestRig(t)
	c := rig.newCache(t, 4)
	ctx := context.Background()
	writer := createWithContent(t, c, testID, []byte("content"))
	if err := c.Free(ctx, testID); !errors.Is(err, ErrBusy) {
		t.Fatalf("Free with open writer: %v", err)
	}
	writer.Close()
	reader, err := c.Open(ctx, testID, 0, model.Local, vaultfile.BodyID{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := c.Free(ctx, testID); err != nil {
		t.Fatalf("Free with only a reader: %v", err)
	}
	if _, err := os.Stat(c.BodyPath(testID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("body still on disk: %v", err)
	}
	if got := readRange(t, reader, 0, 7); string(got) != "content" {
		t.Fatalf("reader after free sees %q", got)
	}
}

func TestFreedSparseReaderIsDetached(t *testing.T) {
	rig := newTestRig(t)
	plaintext := patternedPlaintext(elevenSegments)
	bodyID := rig.publishRemoteBody(t, testID, plaintext)
	c := rig.newCache(t, 4)
	ctx := context.Background()
	reader, err := c.Open(ctx, testID, int64(len(plaintext)), model.CloudOnly, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	assertRange(t, reader, plaintext, 5*segment, 10)
	if err := c.Free(ctx, testID); err != nil {
		t.Fatal(err)
	}
	assertRange(t, reader, plaintext, 5*segment+20, 10)
	if _, err := reader.ReadAt(ctx, make([]byte, 10), 0); !errors.Is(err, ErrDetached) {
		t.Fatalf("missing segment after free: %v", err)
	}
	if rig.bitmaps.has(testID) {
		t.Fatal("bitmap kept after free")
	}
}

func TestCacheDirHoldsNoPlaintext(t *testing.T) {
	rig := newTestRig(t)
	c := rig.newCache(t, 4)
	secretText := "VAULT-PLAINTEXT-CANARY-0123456789"
	local := []byte(strings.Repeat(secretText, 5000))
	createWithContent(t, c, testID, local).Close()
	if _, err := c.Snapshot(context.Background(), "b", testID); err != nil {
		t.Fatal(err)
	}
	remote := append([]byte(nil), local...)
	rand.Read(remote[:16])
	bodyID := rig.publishRemoteBody(t, otherID, remote)
	sparse, err := c.Open(context.Background(), otherID, int64(len(remote)), model.CloudOnly, bodyID)
	if err != nil {
		t.Fatal(err)
	}
	readRange(t, sparse, 0, 1000)
	sparse.Close()
	plaintextcheck.AssertDirClean(t, rig.dir, secretText)
}
