package syncer_test

import (
	"context"
	"testing"

	"vault/internal/devicetest"
)

func deepFolder(a *devicetest.Device) {
	a.Mkdir("/P")
	a.Mkdir("/P/Q")
	a.WriteFile("/P/Q/deep.txt", []byte("deep"))
}

func removeDeepFolder(a *devicetest.Device) {
	a.Remove("/P/Q/deep.txt")
	a.Remove("/P/Q")
	a.Remove("/P")
}

func restoreAll(t *testing.T, d *devicetest.Device) {
	t.Helper()
	for _, id := range trashIDs(t, d) {
		if _, err := d.Workspace.RestoreTrash(context.Background(), id); err != nil {
			t.Fatalf("%s: restore %s: %v", d.ID, id, err)
		}
	}
}

// expectDeepFolderRestores checks the folder delete won everywhere, then that restoring it
// brings back the edit made inside.
func expectDeepFolderRestores(t *testing.T, n *devicetest.Network, restorer *devicetest.Device, devices ...*devicetest.Device) {
	t.Helper()
	tree := assertConverged(t, n, devices...)
	if len(tree) != 0 {
		t.Fatalf("tree = %s, want the folder delete to win", describeTree(tree))
	}
	restoreAll(t, restorer)
	tree = assertConverged(t, n, devices...)
	expectTree(t, tree, map[string]string{"/P": "/", "/P/Q": "/", "/P/Q/deep.txt": "deep edited on b"})
}

func TestOlderEditInsideFolderDeletedElsewhere(t *testing.T) {
	n, a, b := twoDevicesWith(t, deepFolder)
	b.WriteFile("/P/Q/deep.txt", []byte("deep edited on b"))
	removeDeepFolder(a)
	a.Sync()
	b.Sync()
	expectDeepFolderRestores(t, n, a, a, b)
}

func TestNewerEditInsideFolderDeletedElsewhere(t *testing.T) {
	n, a, b := twoDevicesWith(t, deepFolder)
	removeDeepFolder(a)
	b.WriteFile("/P/Q/deep.txt", []byte("deep edited on b"))
	a.Sync()
	b.Sync()
	settle(t, a, b)
	a.Restart()
	b.Restart()
	expectDeepFolderRestores(t, n, a, a, b)
}

func TestFolderDeleteKeepsAnUnsyncedEditInside(t *testing.T) {
	n, a, b := twoDevicesWith(t, deepFolder)
	a.WriteFile("/P/Q/deep.txt", []byte("deep edited on a"))
	removeDeepFolder(a)
	a.Sync()
	if tree := assertConverged(t, n, a, b); len(tree) != 0 {
		t.Fatalf("tree = %s, want everything deleted", describeTree(tree))
	}
	restoreAll(t, b)
	found := false
	for _, content := range assertConverged(t, n, a, b) {
		found = found || content == "deep edited on a"
	}
	if !found {
		t.Fatal("restoring the trash did not bring back the edit made just before the delete")
	}
}

func TestFileEditedThenDeletedBeforeSyncRestoresElsewhere(t *testing.T) {
	n, a, b := twoDevicesWith(t, func(a *devicetest.Device) { a.WriteFile("/doc.txt", []byte("base")) })
	a.WriteFile("/doc.txt", []byte("edited just before the delete"))
	a.Remove("/doc.txt")
	a.Sync()
	assertConverged(t, n, a, b)
	restoreAll(t, b)
	expectTree(t, assertConverged(t, n, a, b), map[string]string{"/doc.txt": "edited just before the delete"})
}

func TestMoveOutOfFolderDeletedElsewhere(t *testing.T) {
	for _, first := range []string{"a", "b"} {
		t.Run(first+" syncs first", func(t *testing.T) {
			n, a, b := twoDevicesWith(t, deepFolder)
			b.Rename("/P/Q/deep.txt", "/deep.txt")
			b.Rename("/P/Q", "/Q")
			b.WriteFile("/Q/new.txt", []byte("new on b"))
			removeDeepFolder(a)
			if first == "a" {
				a.Sync()
			}
			b.Sync()
			a.Sync()
			if tree := assertConverged(t, n, a, b); len(tree) != 0 {
				t.Fatalf("tree = %s, want the folder delete to win", describeTree(tree))
			}
			restoreAll(t, a)
			b.Restart()
			a.Restart()
			expectTree(t, assertConverged(t, n, a, b), map[string]string{"/P": "/", "/P/Q": "/", "/P/Q/deep.txt": "deep",
				"/P/Q/new.txt": "new on b"})
		})
	}
}

func TestThreeDevicesMoveEditAndFolderDelete(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b, c := n.NewDevice(t, "a"), n.NewDevice(t, "b"), n.NewDevice(t, "c")
	a.Mkdir("/F")
	a.WriteFile("/F/doc.txt", []byte("base"))
	a.Sync()
	b.Poll()
	c.Poll()
	a.Rename("/F/doc.txt", "/doc.txt")
	b.WriteFile("/F/doc.txt", []byte("edited on b"))
	c.Remove("/F/doc.txt")
	c.Remove("/F")
	b.Sync()
	c.Sync()
	a.Sync()
	if tree := assertConverged(t, n, a, b, c); len(tree) != 0 {
		t.Fatalf("tree = %s, want the folder delete to win", describeTree(tree))
	}
	assertTrashHolds(t, n, "edited on b")
}

func TestRestoreFolderThenAddInsideBeforeSync(t *testing.T) {
	n, a, b := twoDevicesWith(t, deepFolder)
	removeDeepFolder(a)
	a.Sync()
	b.Poll()
	restoreAll(t, a)
	a.WriteFile("/P/Q/new.txt", []byte("new"))
	a.WriteFile("/P/Q/deep.txt", []byte("deep edited"))
	a.Rename("/P/Q", "/P/R")
	a.Sync()
	expectTree(t, assertConverged(t, n, a, b), map[string]string{"/P": "/", "/P/R": "/", "/P/R/deep.txt": "deep edited",
		"/P/R/new.txt": "new"})
}

func TestRestoreIntoFolderDeletedElsewhere(t *testing.T) {
	n, a, b := twoDevicesWith(t, func(a *devicetest.Device) {
		a.Mkdir("/D")
		a.WriteFile("/D/f.txt", []byte("f"))
		a.WriteFile("/D/g.txt", []byte("g"))
	})
	a.Remove("/D/f.txt")
	a.Sync()
	b.Poll()
	b.Remove("/D/g.txt")
	b.Remove("/D")
	restoreAll(t, a)
	b.Sync()
	a.Sync()
	settle(t, a, b)
	a.Restart()
	b.Restart()
	if tree := assertConverged(t, n, a, b); len(tree) != 0 {
		t.Fatalf("tree = %s, want the restored file inside the deleted folder", describeTree(tree))
	}
	restoreAll(t, b)
	expectTree(t, assertConverged(t, n, a, b), map[string]string{"/D": "/", "/D/f.txt": "f", "/D/g.txt": "g"})
}

func TestEditInsideFolderDeletedHereButNotYetSynced(t *testing.T) {
	n, a, b := twoDevicesWith(t, deepFolder)
	removeDeepFolder(a)
	b.WriteFile("/P/Q/deep.txt", []byte("deep edited on b"))
	b.Sync()
	a.Poll()
	a.Sync()
	settle(t, a, b)
	a.Restart()
	expectDeepFolderRestores(t, n, b, a, b)
}
