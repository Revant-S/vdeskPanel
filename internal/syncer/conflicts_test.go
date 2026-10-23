package syncer_test

import (
	"fmt"
	"testing"

	"vault/internal/devicetest"
)

func twoDevicesWith(t *testing.T, setup func(a *devicetest.Device)) (*devicetest.Network, *devicetest.Device, *devicetest.Device) {
	t.Helper()
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	setup(a)
	a.Sync()
	b.Poll()
	return n, a, b
}

func TestEditorSaveRacingRemoteEditConverges(t *testing.T) {
	for _, newerOn := range []string{"a", "b"} {
		t.Run("newer on "+newerOn, func(t *testing.T) {
			n, a, b := twoDevicesWith(t, func(a *devicetest.Device) { a.WriteFile("/doc.txt", []byte("base")) })
			save := func() {
				a.WriteFile("/.doc.txt.tmp", []byte("saved on a"))
				a.Rename("/.doc.txt.tmp", "/doc.txt")
			}
			if newerOn == "a" {
				b.WriteFile("/doc.txt", []byte("edited on b"))
				save()
			} else {
				save()
				b.WriteFile("/doc.txt", []byte("edited on b"))
			}
			a.Sync()
			b.Sync()
			tree := assertConverged(t, n, a, b)
			winner := map[string]string{"a": "saved on a", "b": "edited on b"}[newerOn]
			expectTree(t, tree, map[string]string{"/doc.txt": winner})
		})
	}
}

func TestVimSaveRacingRemoteEditConverges(t *testing.T) {
	n, a, b := twoDevicesWith(t, func(a *devicetest.Device) { a.WriteFile("/doc.txt", []byte("base")) })
	b.WriteFile("/doc.txt", []byte("edited on b"))
	a.Rename("/doc.txt", "/doc.txt~")
	a.WriteFile("/doc.txt", []byte("saved on a"))
	a.Remove("/doc.txt~")
	b.Sync()
	a.Sync()
	tree := assertConverged(t, n, a, b)
	expectTree(t, tree, map[string]string{"/doc.txt": "saved on a"})
	assertTrashHolds(t, n, "edited on b")
}

func TestMoveVersusMoveNewerWins(t *testing.T) {
	n, a, b := twoDevicesWith(t, func(a *devicetest.Device) {
		a.Mkdir("/A")
		a.Mkdir("/B")
		a.WriteFile("/x.txt", []byte("x"))
	})
	a.Rename("/x.txt", "/A/x.txt")
	b.Rename("/x.txt", "/B/y.txt")
	a.Sync()
	b.Sync()
	tree := assertConverged(t, n, a, b)
	expectTree(t, tree, map[string]string{"/A": "/", "/B": "/", "/B/y.txt": "x"})
}

func TestFileDeleteBeatsNewerMove(t *testing.T) {
	n, a, b := twoDevicesWith(t, func(a *devicetest.Device) {
		a.Mkdir("/A")
		a.WriteFile("/x.txt", []byte("x"))
	})
	a.Remove("/x.txt")
	b.Rename("/x.txt", "/A/x.txt")
	b.Sync()
	a.Sync()
	tree := assertConverged(t, n, a, b)
	expectTree(t, tree, map[string]string{"/A": "/"})
}

func TestMoveIntoFolderDeletedElsewhere(t *testing.T) {
	for _, order := range []string{"delete first", "move first"} {
		t.Run(order, func(t *testing.T) {
			n, a, b := twoDevicesWith(t, func(a *devicetest.Device) {
				a.Mkdir("/Old")
				a.WriteFile("/x.txt", []byte("x"))
			})
			if order == "delete first" {
				a.Remove("/Old")
				b.Rename("/x.txt", "/Old/x.txt")
			} else {
				b.Rename("/x.txt", "/Old/x.txt")
				a.Remove("/Old")
			}
			a.Sync()
			b.Sync()
			tree := assertConverged(t, n, a, b)
			if len(tree) != 0 {
				t.Fatalf("tree = %s, want the folder and its new child in trash", describeTree(tree))
			}
		})
	}
}

func TestFolderRenameWhileOtherDeviceAddsInside(t *testing.T) {
	n, a, b := twoDevicesWith(t, func(a *devicetest.Device) {
		a.Mkdir("/Draft")
		a.WriteFile("/Draft/one.txt", []byte("1"))
	})
	a.Rename("/Draft", "/Final")
	b.WriteFile("/Draft/two.txt", []byte("2"))
	b.WriteFile("/Draft/one.txt", []byte("1 edited"))
	a.Sync()
	b.Sync()
	tree := assertConverged(t, n, a, b)
	expectTree(t, tree, map[string]string{"/Final": "/", "/Final/one.txt": "1 edited", "/Final/two.txt": "2"})
}

func TestThreeDevicesEditTheSameFile(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b, c := n.NewDevice(t, "a"), n.NewDevice(t, "b"), n.NewDevice(t, "c")
	a.WriteFile("/doc.txt", []byte("base"))
	a.Sync()
	b.Poll()
	c.Poll()
	a.WriteFile("/doc.txt", []byte("from a"))
	b.WriteFile("/doc.txt", []byte("from b"))
	c.WriteFile("/doc.txt", []byte("from c"))
	c.Sync()
	a.Sync()
	b.Sync()
	tree := assertConverged(t, n, a, b, c)
	expectTree(t, tree, map[string]string{"/doc.txt": "from c"})
	assertTrashHolds(t, n, "from a")
	assertTrashHolds(t, n, "from b")
}

func TestOfflineDeviceCatchesUpOnManyCommits(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a, b := n.NewDevice(t, "a"), n.NewDevice(t, "b")
	a.Mkdir("/F")
	a.WriteFile("/F/doc.txt", []byte("base"))
	a.Sync()
	b.Poll()
	b.WriteFile("/F/doc.txt", []byte("offline edit on b"))
	b.WriteFile("/F/new.txt", []byte("offline new on b"))
	dir := "/F"
	for i := range 30 {
		a.WriteFile(dir+"/doc.txt", []byte(fmt.Sprintf("a edit %d", i)))
		a.Sync()
		if i == 10 {
			a.Rename("/F", "/G")
			a.Sync()
			dir = "/G"
		}
	}
	tree := assertConverged(t, n, b, a)
	expectTree(t, tree, map[string]string{"/G": "/", "/G/doc.txt": "a edit 29", "/G/new.txt": "offline new on b"})
	assertTrashHolds(t, n, "offline edit on b")
}

func TestDeleteAndRecreateSameNameVersusRemoteEdit(t *testing.T) {
	n, a, b := twoDevicesWith(t, func(a *devicetest.Device) { a.WriteFile("/doc.txt", []byte("base")) })
	b.WriteFile("/doc.txt", []byte("edited on b"))
	b.Sync()
	a.Remove("/doc.txt")
	a.Sync()
	a.WriteFile("/doc.txt", []byte("recreated on a"))
	a.Sync()
	tree := assertConverged(t, n, a, b)
	if len(tree) != 1 && len(tree) != 2 {
		t.Fatalf("tree = %s", describeTree(tree))
	}
}
