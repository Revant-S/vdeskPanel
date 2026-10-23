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
