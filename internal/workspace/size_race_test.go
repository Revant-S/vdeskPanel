package workspace

import (
	"context"
	"sync"
	"testing"

	"vault/internal/fusefs"
)

// Writers through several handles, and renames of the file meanwhile, must leave the index
// with the size of the body: a stale smaller size hides the tail from readers and makes an
// O_APPEND write land on top of it.
func TestConcurrentWritesAndRenamesKeepTheSize(t *testing.T) {
	home := newTestHome(t)
	ctx := context.Background()
	id := home.writeFile(t, "/", "a.log", "")
	const writers, writes, record = 8, 300, 16
	var handles []fusefs.Handle
	for range writers {
		handle, _, err := home.ws.Open(ctx, home.inodeOfPath(t, "/a.log"), true, false, fusefs.Caller{PID: 1})
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, handle)
	}
	var group sync.WaitGroup
	for w, handle := range handles {
		group.Go(func() {
			for i := range writes {
				offset := int64((i*writers + w) * record)
				if _, err := handle.WriteAt(ctx, make([]byte, record), offset); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	stop := make(chan struct{})
	renamed := make(chan struct{})
	go func() {
		defer close(renamed)
		names := [2]string{"a.log", "b.log"}
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := home.ws.Rename(ctx, rootIno, names[n%2], rootIno, names[(n+1)%2], false); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	group.Wait()
	close(stop)
	<-renamed
	for _, handle := range handles {
		if err := handle.Release(ctx); err != nil {
			t.Fatal(err)
		}
	}
	want := int64(writers * writes * record)
	if got := home.object(t, id).Size; got != want {
		t.Fatalf("index size %d after concurrent writes, body is %d", got, want)
	}
}
