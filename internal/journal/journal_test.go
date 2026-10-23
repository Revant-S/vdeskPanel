package journal

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"vault/internal/crypto"
	"vault/internal/model"
)

func TestClockIsMonotonicWhenWallGoesBack(t *testing.T) {
	walls := []int64{1000, 1000, 900, 2000}
	position := 0
	wall := func() time.Time { w := walls[min(position, len(walls)-1)]; position++; return time.UnixMilli(w) }
	var persisted model.Stamp
	clock := NewClock("d-a", wall, model.Stamp{}, func(s model.Stamp) { persisted = s }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var previous model.Stamp
	for range walls {
		next := clock.Next()
		if !next.After(previous) {
			t.Fatalf("stamp %v not after %v", next, previous)
		}
		previous = next
	}
	if persisted != previous {
		t.Fatal("clock state not persisted")
	}
}

func TestClockCounterOverflowBumpsWall(t *testing.T) {
	wall := func() time.Time { return time.UnixMilli(5) }
	clock := NewClock("d-a", wall, model.Stamp{WallMillis: 5, Counter: maxCounter, Device: "d-a"}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if next := clock.Next(); next.WallMillis != 6 || next.Counter != 0 {
		t.Fatalf("overflow stamp = %v", next)
	}
}

func TestClockObserveAdvancesButClampsFarFuture(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	clock := NewClock("d-a", func() time.Time { return now }, model.Stamp{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	clock.Observe(model.Stamp{WallMillis: now.UnixMilli() + 5000, Counter: 3, Device: "d-b"})
	if next := clock.Next(); next.WallMillis != now.UnixMilli()+5000 || next.Counter != 4 {
		t.Fatalf("after observe, Next = %v", next)
	}
	clock.Observe(model.Stamp{WallMillis: now.Add(48 * time.Hour).UnixMilli(), Device: "d-b"})
	if last := clock.Last(); last.WallMillis != now.Add(time.Hour).UnixMilli() {
		t.Fatalf("far-future observe moved clock to %v", last)
	}
}

func TestCodecRoundTripAndBinding(t *testing.T) {
	codec, _ := NewCodec(rand.Reader)
	defer codec.Close()
	keys := keysOf(t, sharedKeyring(t))
	id, _ := NewCommitID(rand.Reader)
	commit := Commit{ID: id, Device: "d-a", DeviceName: "amber", Clock: model.Stamp{WallMillis: 7, Device: "d-a"},
		Ops: []Op{{Kind: OpCreate, ObjectID: "aa", Path: "/Notes/gate.md", Size: 2048, Version: model.Stamp{WallMillis: 6, Device: "d-a"}}}}
	sealed, err := codec.SealCommit(commit, keys.Log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "gate.md") {
		t.Fatal("sealed commit contains a plaintext path")
	}
	opened, err := codec.OpenCommit(id, sealed, keys.Log)
	if err != nil || opened.Ops[0].Path != "/Notes/gate.md" || opened.Ops[0].Version != commit.Ops[0].Version || opened.Clock != commit.Clock {
		t.Fatalf("OpenCommit = %+v, %v", opened, err)
	}
	otherID, _ := NewCommitID(rand.Reader)
	if _, err := codec.OpenCommit(otherID, sealed, keys.Log); !errors.Is(err, crypto.ErrDecrypt) {
		t.Fatalf("commit opened under another id: %v", err)
	}
	pack, _ := codec.SealPack("pack-a", []Commit{commit}, keys.Log)
	if commits, err := codec.OpenPack("pack-a", pack, keys.Log); err != nil || len(commits) != 1 {
		t.Fatalf("OpenPack = %v, %v", commits, err)
	}
	if _, err := codec.OpenPack("pack-b", pack, keys.Log); err == nil {
		t.Fatal("pack opened under another name")
	}
}

func TestRecordChainsParentsAndMergesOtherDevices(t *testing.T) {
	remote := newFakeLogRemote()
	wall := steppingWall()
	a := newTestDevice(t, remote, "d-a", 500, wall)
	b := newTestDevice(t, remote, "d-b", 500, wall)

	first := a.record(t, Op{Kind: OpCreate, ObjectID: "aa", Path: "/a.txt"})
	second := a.record(t, Op{Kind: OpModify, ObjectID: "aa", Path: "/a.txt"})
	if len(first.Parents) != 0 || len(second.Parents) != 1 || second.Parents[0] != first.ID {
		t.Fatalf("parents: first %v second %v", first.Parents, second.Parents)
	}
	if got := b.ingestFrom(t, a); len(got) != 2 {
		t.Fatalf("B ingested %d commits", len(got))
	}
	if again := b.ingestFrom(t, a); len(again) != 0 {
		t.Fatalf("second ingest stored %d commits", len(again))
	}
	b.markAllApplied(t)
	merge := b.record(t, Op{Kind: OpCreate, ObjectID: "bb", Path: "/b.txt"})
	if len(merge.Parents) != 1 || merge.Parents[0] != second.ID {
		t.Fatalf("merge parents = %v, want [%s]", merge.Parents, second.ID)
	}
	if !merge.Clock.After(second.Clock) {
		t.Fatal("merge commit not after the commit it follows")
	}
	next := b.record(t, Op{Kind: OpDelete, ObjectID: "bb", Path: "/b.txt"})
	if len(next.Parents) != 1 || next.Parents[0] != merge.ID {
		t.Fatalf("follow-up parents = %v, want only own previous", next.Parents)
	}
}

func TestRecordReplayAfterLostAcknowledgementWritesOnce(t *testing.T) {
	remote := newFakeLogRemote()
	a := newTestDevice(t, remote, "d-a", 500, steppingWall())
	remote.failCreates = 1
	commitID, _ := NewCommitID(rand.Reader)
	ops := []Op{{Kind: OpCreate, ObjectID: "aa", Path: "/a.txt"}}
	if _, err := a.journal.Record(context.Background(), commitID, "drive-commit", ops); err == nil {
		t.Fatal("first attempt should report the lost acknowledgement")
	}
	replayed, err := a.journal.Record(context.Background(), commitID, "drive-commit", ops)
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.journal.Record(context.Background(), commitID, "drive-commit", ops)
	if err != nil || again.Clock != replayed.Clock {
		t.Fatalf("third call = %v, %v", again, err)
	}
	files, _ := remote.ListFolder(context.Background(), a.folder)
	if len(files) != 1 || replayed.ID != commitID || replayed.Ops[0].Path != "/a.txt" {
		t.Fatalf("files on drive = %d, replayed = %+v", len(files), replayed)
	}
}

func TestPackReplacesSinglesWithoutGaps(t *testing.T) {
	remote := newFakeLogRemote()
	wall := steppingWall()
	a := newTestDevice(t, remote, "d-a", 5, wall)
	b := newTestDevice(t, remote, "d-b", 5, wall)
	var ids []string
	for i := 0; i < 6; i++ {
		ids = append(ids, a.record(t, Op{Kind: OpCreate, ObjectID: "aa"}).ID)
	}
	if err := a.journal.PackIfDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	files, _ := remote.ListFolder(context.Background(), a.folder)
	if len(files) != 1 || !strings.HasSuffix(files[0].Name, packSuffix) {
		t.Fatalf("after packing, folder holds %v", files)
	}
	got := b.ingestFrom(t, a)
	if len(got) != 6 {
		t.Fatalf("B ingested %d commits from the pack", len(got))
	}
	record, err := b.journal.storedCommit(context.Background(), ids[3])
	if err != nil {
		t.Fatal(err)
	}
	if commit, err := b.journal.OpenRecord(record); err != nil || commit.ID != ids[3] {
		t.Fatalf("unpacked commit = %v, %v", commit, err)
	}
	if err := a.journal.PackIfDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if files, _ := remote.ListFolder(context.Background(), a.folder); len(files) != 1 {
		t.Fatalf("packing again with no singles changed the folder: %v", files)
	}
}

func TestIngestReportsFilesDeletedByPacking(t *testing.T) {
	remote := newFakeLogRemote()
	a := newTestDevice(t, remote, "d-a", 500, steppingWall())
	b := newTestDevice(t, remote, "d-b", 500, steppingWall())
	a.record(t, Op{Kind: OpCreate, ObjectID: "aa"})
	files, _ := remote.ListFolder(context.Background(), a.folder)
	remote.Delete(context.Background(), files[0].ID)
	_, err := b.journal.Ingest(context.Background(), files, func(err error) bool { return errors.Is(err, errGone) })
	if !errors.Is(err, ErrLogFileGone) {
		t.Fatalf("error = %v, want ErrLogFileGone", err)
	}
}

func keysOf(t *testing.T, keyring *crypto.Keyring) *crypto.Keys {
	t.Helper()
	var keys *crypto.Keys
	if err := keyring.Use(func(k *crypto.Keys) error { keys = k; return nil }); err != nil {
		t.Fatal(err)
	}
	return keys
}
