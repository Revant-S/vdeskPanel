package index

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bits-and-blooms/bitset"
	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/plaintextcheck"
	"github.com/google/go-cmp/cmp"
)

func testSealer(t *testing.T) *crypto.IndexSealer {
	t.Helper()
	_, keyring, err := crypto.CreateVault([]byte("correct horse battery"), crypto.KDFParams{Time: 1, MemoryKiB: 8 * 1024, Threads: 1}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(keyring.Destroy)
	return crypto.NewIndexSealer(keyring, rand.Reader)
}

func openTestStore(t *testing.T, path string, sealer Sealer) *Store {
	t.Helper()
	store, err := Open(context.Background(), path, sealer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func update(t *testing.T, store *Store, fn func(*Tx) error) {
	t.Helper()
	if err := store.Update(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func view(t *testing.T, store *Store, fn func(*Tx) error) {
	t.Helper()
	if err := store.View(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func stamp(wall int64) model.Stamp {
	return model.Stamp{WallMillis: wall, Counter: 1, Device: "d-a"}
}

func TestReopenKeepsSchemaAndRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	sealer := testSealer(t)
	first := openTestStore(t, path, sealer)
	update(t, first, func(tx *Tx) error { return tx.SetKV("page-token", []byte("42")) })
	first.Close()
	second := openTestStore(t, path, sealer)
	view(t, second, func(tx *Tx) error {
		value, ok, err := tx.KV("page-token")
		if err != nil || !ok || string(value) != "42" {
			t.Fatalf("KV = %q, %v, %v", value, ok, err)
		}
		return nil
	})
}

func TestObjectRoundTripAndQueries(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "index.db"), testSealer(t))
	object := Object{
		ID: "aa", ParentID: model.RootID, Kind: model.KindFile, EncName: "encrypted", DriveID: "drive-1",
		Size: 2048, ModTime: time.Unix(1700000000, 5), Executable: true, ContentMAC: []byte{1, 2},
		BodyID: []byte{9}, BodyMD5: "md5", ContentVer: stamp(3), PlacementVer: stamp(2), State: model.Local,
		DeferredContent: []byte(`{"pending":"yes"}`),
	}
	folder := Object{ID: "bb", ParentID: model.RootID, Kind: model.KindFolder, EncName: "dir", State: model.Local}
	update(t, store, func(tx *Tx) error {
		if err := tx.PutObject(object); err != nil {
			return err
		}
		return tx.PutObject(folder)
	})
	view(t, store, func(tx *Tx) error {
		got, err := tx.Object("aa")
		if err != nil {
			return err
		}
		if diff := cmp.Diff(object, got); diff != "" {
			t.Fatalf("object mismatch (-want +got):\n%s", diff)
		}
		children, _ := tx.ChildrenOf(model.RootID)
		byMAC, _ := tx.ObjectsByContentMAC([]byte{1, 2})
		byDrive, _ := tx.ObjectsByDriveID("drive-1")
		if len(children) != 2 || len(byMAC) != 1 || len(byDrive) != 1 {
			t.Fatalf("children=%d byMAC=%d byDrive=%d", len(children), len(byMAC), len(byDrive))
		}
		count := 0
		for _, err := range tx.AllObjects() {
			if err != nil {
				return err
			}
			count++
		}
		if count != 2 {
			t.Fatalf("AllObjects yielded %d", count)
		}
		_, err = tx.Object("missing")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing object error = %v", err)
		}
		return nil
	})
	update(t, store, func(tx *Tx) error {
		if err := tx.SetLocalState("aa", model.Dirty); err != nil {
			return err
		}
		dirty, _ := tx.ObjectsInState(model.Dirty)
		if len(dirty) != 1 {
			t.Fatalf("dirty objects = %d", len(dirty))
		}
		return tx.DeleteObject("aa")
	})
}

func TestPendingOpsQueue(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "index.db"), testSealer(t))
	var seqs []int64
	update(t, store, func(tx *Tx) error {
		for i := int64(1); i <= 3; i++ {
			seq, err := tx.AppendOp(PendingOp{ObjectID: "aa", Kind: "modify", Payload: []byte("/Notes/gate.md"), Stamp: stamp(i), NotBefore: time.Unix(i, 0)})
			if err != nil {
				return err
			}
			seqs = append(seqs, seq)
		}
		return tx.AssignBatch(seqs[:2], "batch-1")
	})
	view(t, store, func(tx *Tx) error {
		unbatched, _ := tx.UnbatchedOps()
		batched, _ := tx.OpsOfBatch("batch-1")
		count, _ := tx.CountPendingOps()
		if len(unbatched) != 1 || len(batched) != 2 || count != 3 {
			t.Fatalf("unbatched=%d batched=%d count=%d", len(unbatched), len(batched), count)
		}
		if string(batched[0].Payload) != "/Notes/gate.md" || batched[0].Stamp != stamp(1) {
			t.Fatalf("op = %+v", batched[0])
		}
		return nil
	})
	update(t, store, func(tx *Tx) error {
		if err := tx.RescheduleOp(seqs[0], 2, time.Unix(99, 0)); err != nil {
			return err
		}
		if err := tx.DeleteOpsOfBatch("batch-1"); err != nil {
			return err
		}
		return tx.DeleteOps(seqs[2:])
	})
	view(t, store, func(tx *Tx) error {
		remaining, _ := tx.UnbatchedOps()
		if len(remaining) != 1 || remaining[0].Attempts != 2 || !remaining[0].NextAttempt.Equal(time.Unix(99, 0)) {
			t.Fatalf("remaining = %+v", remaining)
		}
		return nil
	})
}

func TestBatchesAndTrash(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "index.db"), testSealer(t))
	batch := Batch{ID: "b1", CommitID: "c1", Payload: []byte("net ops"), State: "persisted", CreatedAt: time.Unix(5, 0)}
	record := TrashRecord{ID: "t-1", ObjectID: "aa", OriginalPath: "/Photos/cat.jpg", DeletedAt: time.Unix(10, 0),
		Device: "d-a", Reason: "moved out", Size: 10, FileCount: 1, ContentMAC: []byte{7}, DriveID: "drive-t"}
	update(t, store, func(tx *Tx) error {
		if err := tx.PutBatch(batch); err != nil {
			return err
		}
		return tx.PutTrash(record)
	})
	view(t, store, func(tx *Tx) error {
		gotBatch, err := tx.Batch("b1")
		if err != nil {
			return err
		}
		if diff := cmp.Diff(batch, gotBatch); diff != "" {
			t.Fatalf("batch mismatch:\n%s", diff)
		}
		gotTrash, err := tx.Trash("t-1")
		if err != nil {
			return err
		}
		if diff := cmp.Diff(record, gotTrash); diff != "" {
			t.Fatalf("trash mismatch:\n%s", diff)
		}
		byMAC, _ := tx.TrashByContentMAC([]byte{7})
		all, _ := tx.TrashRecords()
		if len(byMAC) != 1 || len(all) != 1 {
			t.Fatal("trash queries wrong")
		}
		return nil
	})
	update(t, store, func(tx *Tx) error {
		if err := tx.DeleteBatch("b1"); err != nil {
			return err
		}
		return tx.DeleteTrash("t-1")
	})
}

func TestCommitOrderingAndHeads(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "index.db"), testSealer(t))
	commits := []CommitRecord{
		{ID: "a1", Device: "d-a", Clock: model.Stamp{WallMillis: 1, Device: "d-a"}, Own: true, Applied: true, Sealed: []byte{1}},
		{ID: "b1", Device: "d-b", Clock: model.Stamp{WallMillis: 2, Device: "d-b"}, Applied: true, Sealed: []byte{1}},
		{ID: "b2", Device: "d-b", Clock: model.Stamp{WallMillis: 3, Device: "d-b"}, Parents: []string{"b1"}, Applied: true, Sealed: []byte{1}},
		{ID: "c1", Device: "d-c", Clock: model.Stamp{WallMillis: 4, Device: "d-c"}, Sealed: []byte{1}},
	}
	update(t, store, func(tx *Tx) error {
		for _, c := range append(commits, commits[0]) {
			if err := tx.PutCommit(c); err != nil {
				return err
			}
		}
		return nil
	})
	view(t, store, func(tx *Tx) error {
		page, _ := tx.CommitsBefore(model.Stamp{}, 2)
		older, _ := tx.CommitsBefore(page[1].Clock, 10)
		if len(page) != 2 || page[0].ID != "c1" || len(older) != 2 || older[0].ID != "b1" {
			t.Fatalf("paging wrong: %v %v", page, older)
		}
		unapplied, _ := tx.UnappliedCommits()
		heads, _ := tx.UnreferencedHeads("d-a")
		if len(unapplied) != 1 || len(heads) != 1 || heads[0].ID != "b2" {
			t.Fatalf("unapplied=%v heads=%v", unapplied, heads)
		}
		latest, _ := tx.LatestCommitOf("d-b")
		count, _ := tx.CountCommitsOf("d-b")
		if latest.ID != "b2" || count != 2 || latest.Parents[0] != "b1" {
			t.Fatalf("latest=%v count=%d", latest, count)
		}
		return nil
	})
	update(t, store, func(tx *Tx) error {
		if err := tx.MarkDeviceReferenced("d-b", model.Stamp{WallMillis: 3, Device: "d-b"}); err != nil {
			return err
		}
		return tx.MarkCommitApplied("c1")
	})
	view(t, store, func(tx *Tx) error {
		heads, _ := tx.UnreferencedHeads("d-a")
		if len(heads) != 1 || heads[0].ID != "c1" {
			t.Fatalf("heads after reference = %v", heads)
		}
		return nil
	})
}

func TestSegmentBitmapPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	sealer := testSealer(t)
	store := openTestStore(t, path, sealer)
	present := bitset.New(200).Set(0).Set(129)
	if err := store.PutSegmentBitmap(context.Background(), "aa", present); err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened := openTestStore(t, path, sealer)
	got, err := reopened.SegmentBitmap(context.Background(), "aa")
	if err != nil || !got.Test(129) || !got.Test(0) || got.Count() != 2 {
		t.Fatalf("bitmap = %v, %v", got, err)
	}
	if missing, err := reopened.SegmentBitmap(context.Background(), "none"); err != nil || missing != nil {
		t.Fatalf("missing bitmap = %v, %v", missing, err)
	}
	if err := reopened.PutSegmentBitmap(context.Background(), "aa", nil); err != nil {
		t.Fatal(err)
	}
	if deleted, err := reopened.SegmentBitmap(context.Background(), "aa"); err != nil || deleted != nil {
		t.Fatalf("deleted bitmap = %v, %v", deleted, err)
	}
}

func TestSealedColumnsHoldNoPlaintext(t *testing.T) {
	dir := t.TempDir()
	sealer := testSealer(t)
	store := openTestStore(t, filepath.Join(dir, "index.db"), sealer)
	update(t, store, func(tx *Tx) error {
		if _, err := tx.AppendOp(PendingOp{ObjectID: "aa", Kind: "create", Payload: []byte("/Secret/plans.txt"), Stamp: stamp(1)}); err != nil {
			return err
		}
		if err := tx.PutTrash(TrashRecord{ID: "t", OriginalPath: "/Secret/diary.txt"}); err != nil {
			return err
		}
		return tx.PutObject(Object{ID: "aa", ParentID: model.RootID, Kind: model.KindFile, State: model.Local, DeferredContent: []byte("Secret/other")})
	})
	plaintextcheck.AssertDirClean(t, dir, "Secret", "plans.txt", "diary.txt")

	wrongKey := openTestStore(t, filepath.Join(dir, "index.db"), testSealer(t))
	err := wrongKey.View(context.Background(), func(tx *Tx) error {
		_, err := tx.Trash("t")
		return err
	})
	if !errors.Is(err, crypto.ErrDecrypt) {
		t.Fatalf("reading with another key = %v, want ErrDecrypt", err)
	}
}

// A killed process must not lose committed rows; the child exits without closing the store.
func TestCommittedRowsSurviveAbruptExit(t *testing.T) {
	if path := os.Getenv("VAULT_INDEX_CRASH_PATH"); path != "" {
		store, err := Open(context.Background(), path, plainSealer{})
		if err != nil {
			os.Exit(2)
		}
		err = store.Update(context.Background(), func(tx *Tx) error { return tx.SetKV("survives", []byte("yes")) })
		if err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "index.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCommittedRowsSurviveAbruptExit$")
	cmd.Env = append(os.Environ(), "VAULT_INDEX_CRASH_PATH="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, output)
	}
	store := openTestStore(t, path, plainSealer{})
	view(t, store, func(tx *Tx) error {
		value, ok, err := tx.KV("survives")
		if !ok || string(value) != "yes" {
			t.Fatalf("row lost after abrupt exit: %q %v %v", value, ok, err)
		}
		return err
	})
}

type plainSealer struct{}

func (plainSealer) Seal(_, plaintext []byte) ([]byte, error) { return plaintext, nil }
func (plainSealer) Open(_, sealed []byte) ([]byte, error)    { return sealed, nil }
