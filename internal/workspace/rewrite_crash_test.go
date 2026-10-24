package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"testing"

	"vault/internal/crypto"
	"vault/internal/fusefs"
	"vault/internal/model"
	"vault/internal/vaultfile"
)

var errKilled = errors.New("killed")

// dyingFile lets the first writes through and then fails every write and truncate, like a
// process killed between two steps of a body rewrite.
type dyingFile struct {
	*os.File
	writesLeft int
}

func (f *dyingFile) WriteAt(p []byte, offset int64) (int, error) {
	if f.writesLeft == 0 {
		return 0, errKilled
	}
	f.writesLeft--
	return f.File.WriteAt(p, offset)
}

func (f *dyingFile) Truncate(int64) error { return errKilled }

// rewriteThenCrash runs a body rewrite on the cached file, kills it after the given number
// of segment writes, and restarts the workspace.
func (h *testHome) rewriteThenCrash(t *testing.T, id model.ObjectID, size int64, writes int, rewrite func(*vaultfile.Body) error) {
	t.Helper()
	file, err := os.OpenFile(h.cache.BodyPath(id), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = testKeyring().Use(func(k *crypto.Keys) error {
		body, err := vaultfile.OpenBody(&dyingFile{File: file, writesLeft: writes}, k.Content, size, rand.Reader)
		if err != nil {
			return err
		}
		defer body.Close()
		return rewrite(body)
	})
	if !errors.Is(err, errKilled) {
		t.Fatalf("rewrite was not interrupted: %v", err)
	}
	h.restart(t)
}

func (h *testHome) readAll(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	ctx := context.Background()
	handle, _, err := h.ws.Open(ctx, h.inodeOfPath(t, path), false, false, fusefs.Caller{})
	if err != nil {
		return nil, err
	}
	defer handle.Release(ctx)
	buf := make([]byte, 1<<20)
	n, err := handle.ReadAt(ctx, buf, 0)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return buf[:n], err
}

func TestCrashMidAppendKeepsTheFileReadable(t *testing.T) {
	home := newTestHome(t)
	old := make([]byte, 100_000)
	rand.Read(old)
	id := home.writeFile(t, "/", "log.bin", string(old))
	more := bytes.Repeat([]byte("n"), 150_000)
	home.rewriteThenCrash(t, id, int64(len(old)), 1, func(body *vaultfile.Body) error {
		_, err := body.WriteAt(more, int64(len(old)))
		return err
	})

	got, err := home.readAll(t, "/log.bin")
	if err != nil {
		t.Fatalf("after a crash mid-append log.bin fails to read: %v", err)
	}
	if !bytes.HasPrefix(got, old) || !bytes.HasPrefix(more, got[len(old):]) {
		t.Fatalf("after the crash log.bin holds %d bytes that are neither old nor partly appended", len(got))
	}
	if _, err := home.cache.Snapshot(context.Background(), "batch", id); err != nil {
		t.Fatalf("snapshot for upload: %v", err)
	}
}

func TestCrashMidTruncateToZeroKeepsTheFileReadable(t *testing.T) {
	home := newTestHome(t)
	old := make([]byte, 100_000)
	rand.Read(old)
	id := home.writeFile(t, "/", "doc.odt", string(old))
	home.rewriteThenCrash(t, id, int64(len(old)), 1, func(body *vaultfile.Body) error { return body.Truncate(0) })

	got, err := home.readAll(t, "/doc.odt")
	if err != nil {
		t.Fatalf("after a crash mid-truncate doc.odt fails to read: %v", err)
	}
	if len(got) != 0 && !bytes.Equal(got, old) {
		t.Fatalf("after the crash doc.odt holds %d bytes, neither empty nor the old content", len(got))
	}
	if _, err := home.cache.Snapshot(context.Background(), "batch", id); err != nil {
		t.Fatalf("snapshot for upload: %v", err)
	}
}

func TestCrashMidShrinkKeepsTheFileReadable(t *testing.T) {
	for _, writes := range []int{0, 1} {
		home := newTestHome(t)
		old := make([]byte, 200_000)
		rand.Read(old)
		id := home.writeFile(t, "/", "data.bin", string(old))
		const target = 70_000
		if err := home.ws.recordShrink(context.Background(), id, target); err != nil {
			t.Fatal(err)
		}
		home.rewriteThenCrash(t, id, int64(len(old)), writes, func(body *vaultfile.Body) error { return body.Truncate(target) })

		got, err := home.readAll(t, "/data.bin")
		if err != nil {
			t.Fatalf("crash after %d writes: data.bin fails to read: %v", writes, err)
		}
		if !bytes.Equal(got, old) && !bytes.Equal(got, old[:target]) {
			t.Fatalf("crash after %d writes: data.bin holds %d bytes, neither the old content nor the shrunk one", writes, len(got))
		}
		if _, err := home.cache.Snapshot(context.Background(), "batch", id); err != nil {
			t.Fatalf("crash after %d writes: snapshot for upload: %v", writes, err)
		}
	}
}
