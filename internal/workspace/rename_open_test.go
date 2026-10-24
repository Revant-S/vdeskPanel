package workspace

import (
	"bytes"
	"context"
	"testing"

	"vault/internal/fusefs"
)

// A program that renames its temp file over the target before closing it, and keeps
// writing, shares the body with anyone who opens the target meanwhile.
func TestRenameOverWhileTempStillOpenKeepsOneBody(t *testing.T) {
	home := newTestHome(t)
	ctx := context.Background()
	target := home.writeFile(t, "/", "report.txt", "v1")
	home.markSynced(t, target, "drive-v1")
	_, writer, err := home.ws.Create(ctx, rootIno, ".report.tmp", false, fusefs.Caller{PID: 1})
	if err != nil {
		t.Fatal(err)
	}
	first := bytes.Repeat([]byte("a"), 100_000)
	if _, err := writer.WriteAt(ctx, first, 0); err != nil {
		t.Fatal(err)
	}
	if err := home.ws.Rename(ctx, rootIno, ".report.tmp", rootIno, "report.txt", false); err != nil {
		t.Fatal(err)
	}
	reader, _, err := home.ws.Open(ctx, home.inodeOfPath(t, "/report.txt"), false, false, fusefs.Caller{PID: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release(ctx)
	second := bytes.Repeat([]byte("b"), 100_000)
	if _, err := writer.WriteAt(ctx, second, int64(len(first))); err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte{}, first...), second...)
	got := make([]byte, len(want))
	if n, err := reader.ReadAt(ctx, got, 0); err != nil || n != len(want) {
		t.Fatalf("reader opened after the rename read %d bytes, err %v; want %d", n, err, len(want))
	}
	if !bytes.Equal(got, want) {
		t.Fatal("reader sees different bytes from the writer")
	}
	if err := writer.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := home.readAll(t, "/report.txt"); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("after close report.txt reads %d bytes, err %v", len(got), err)
	}
}
