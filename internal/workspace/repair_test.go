package workspace

import (
	"context"
	"strings"
	"testing"

	"vault/internal/index"
	"vault/internal/model"
)

func TestFolderMovedToDesktopTrashTakesItsContents(t *testing.T) {
	home := newTestHome(t)
	home.mkdir(t, "/", "Album")
	home.writeFile(t, "/Album", "a.jpg", "a")
	home.mkdir(t, "/", ".Trash-1000")
	home.mkdir(t, "/.Trash-1000", "files")
	if err := home.ws.Rename(context.Background(), rootIno, "Album", home.inodeOfPath(t, "/.Trash-1000/files"), "Album", false); err != nil {
		t.Fatal(err)
	}
	home.restart(t)
	deleted := 0
	for _, op := range home.pendingOps(t) {
		if op.Kind == model.LocalDelete {
			deleted++
		}
	}
	if deleted != 2 {
		t.Fatalf("delete ops = %d, want folder and its file", deleted)
	}
	if _, ok := home.ws.tree.Resolve("/Album/a.jpg"); ok {
		t.Fatal("file inside the trashed folder is still in the vault")
	}
}

func TestOrphansFromOlderBuildsAreRecovered(t *testing.T) {
	home := newTestHome(t)
	home.mkdir(t, "/", "Album")
	home.writeFile(t, "/Album", "a.jpg", "kept")
	entry, _ := home.ws.tree.Resolve("/Album")
	err := home.index.Update(context.Background(), func(tx *index.Tx) error {
		object, err := tx.Object(entry.ID)
		if err != nil {
			return err
		}
		object.DeleteVer = home.clock.Next()
		return tx.PutObject(object)
	})
	if err != nil {
		t.Fatal(err)
	}
	home.restart(t)
	children := home.ws.tree.Children(model.RootID)
	if len(children) != 1 || !strings.HasPrefix(children[0].Name, "Recovered") {
		t.Fatalf("root holds %v", children)
	}
	if got := home.readFile(t, "/"+children[0].Name+"/a.jpg"); got != "kept" {
		t.Fatalf("recovered file reads %q", got)
	}
}
