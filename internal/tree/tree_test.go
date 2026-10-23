package tree

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"vault/internal/model"
)

func folder(id, parent model.ObjectID, name string) model.Entry {
	return model.Entry{ID: id, ParentID: parent, Name: name, Kind: model.KindFolder, State: model.Local}
}

func file(id, parent model.ObjectID, name string, size int64, state model.LocalState) model.Entry {
	return model.Entry{ID: id, ParentID: parent, Name: name, Kind: model.KindFile, Size: size, State: state}
}

func mustInsert(t *testing.T, tr *Tree, entries ...model.Entry) {
	t.Helper()
	for _, entry := range entries {
		if err := tr.Insert(entry); err != nil {
			t.Fatalf("insert %s: %v", entry.ID, err)
		}
	}
}

// sampleTree is /docs/{a.txt, sub/{b.txt}} and /music.
func sampleTree(t *testing.T) *Tree {
	tr := New()
	mustInsert(t, tr,
		folder("docs", model.RootID, "docs"),
		folder("music", model.RootID, "music"),
		file("a", "docs", "a.txt", 10, model.Local),
		folder("sub", "docs", "sub"),
		file("b", "sub", "b.txt", 20, model.CloudOnly),
	)
	return tr
}

func TestInsertDuplicateNameIsErrExist(t *testing.T) {
	tr := sampleTree(t)
	err := tr.Insert(file("a2", "docs", "a.txt", 1, model.Local))
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate name: %v", err)
	}
	if err := tr.Insert(file("a", "music", "other", 1, model.Local)); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate id: %v", err)
	}
}

func TestInsertRejectsMissingParentFileParentAndBadNames(t *testing.T) {
	tr := sampleTree(t)
	if err := tr.Insert(file("x", "nope", "x", 1, model.Local)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing parent: %v", err)
	}
	if err := tr.Insert(file("x", "a", "x", 1, model.Local)); !errors.Is(err, ErrNotFolder) {
		t.Fatalf("file parent: %v", err)
	}
	for _, name := range []string{"", ".", "..", "a/b"} {
		if err := tr.Insert(file("x", "docs", name, 1, model.Local)); !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("name %q: %v", name, err)
		}
	}
}

func TestMoveIntoOwnSubtreeIsRefused(t *testing.T) {
	tr := sampleTree(t)
	for _, target := range []model.ObjectID{"docs", "sub"} {
		if err := tr.Move("docs", target, "docs"); !errors.Is(err, ErrMoveIntoSubtree) {
			t.Fatalf("move docs into %s: %v", target, err)
		}
	}
	if err := tr.Move(model.RootID, "music", "root"); !errors.Is(err, ErrRootImmutable) {
		t.Fatalf("move root: %v", err)
	}
}

func TestMoveRenamesAndRefusesOccupiedName(t *testing.T) {
	tr := sampleTree(t)
	if err := tr.Move("sub", "music", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Move("a", "music", "a.txt"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("move onto occupied name: %v", err)
	}
	if err := tr.Move("sub", "music", "a.txt"); err != nil {
		t.Fatalf("move onto itself: %v", err)
	}
	if path, _ := tr.PathOf("b"); path != "/music/a.txt/b.txt" {
		t.Fatalf("path after move = %q", path)
	}
	if _, ok := tr.Lookup("docs", "sub"); ok {
		t.Fatal("old name still resolves")
	}
}

func TestPathOfAndResolveAgree(t *testing.T) {
	tr := sampleTree(t)
	for _, id := range []model.ObjectID{model.RootID, "docs", "music", "a", "sub", "b"} {
		path, err := tr.PathOf(id)
		if err != nil {
			t.Fatal(err)
		}
		entry, ok := tr.Resolve(path)
		if !ok || entry.ID != id {
			t.Fatalf("Resolve(%q) = %v, %v; want %s", path, entry.ID, ok, id)
		}
	}
	if path, _ := tr.PathOf("b"); path != "/docs/sub/b.txt" {
		t.Fatalf("PathOf(b) = %q", path)
	}
	if _, ok := tr.Resolve("docs/sub"); ok {
		t.Fatal("relative path resolved")
	}
	if _, err := tr.PathOf("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("PathOf(missing): %v", err)
	}
}

func TestRemoveReturnsSubtree(t *testing.T) {
	tr := sampleTree(t)
	removed, err := tr.Remove("docs")
	if err != nil {
		t.Fatal(err)
	}
	var ids []model.ObjectID
	for _, entry := range removed {
		ids = append(ids, entry.ID)
	}
	if fmt.Sprint(ids) != "[docs a sub b]" {
		t.Fatalf("removed = %v", ids)
	}
	for _, id := range ids {
		if _, ok := tr.Get(id); ok {
			t.Fatalf("%s still present", id)
		}
	}
	if children := tr.Children(model.RootID); len(children) != 1 || children[0].ID != "music" {
		t.Fatalf("root children = %v", children)
	}
}

func TestRebindKeepsPlaceAndChildren(t *testing.T) {
	tr := sampleTree(t)
	if err := tr.Rebind("sub", "sub2"); err != nil {
		t.Fatal(err)
	}
	if entry, ok := tr.Lookup("docs", "sub"); !ok || entry.ID != "sub2" {
		t.Fatalf("lookup after rebind = %v, %v", entry, ok)
	}
	if child, _ := tr.Get("b"); child.ParentID != "sub2" {
		t.Fatalf("child parent = %s", child.ParentID)
	}
	if path, _ := tr.PathOf("b"); path != "/docs/sub/b.txt" {
		t.Fatalf("path after rebind = %q", path)
	}
	if _, ok := tr.Get("sub"); ok {
		t.Fatal("old id still present")
	}
	if err := tr.Rebind("a", "b"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("rebind onto existing id: %v", err)
	}
}

func TestUpdateRefusesPlacementChange(t *testing.T) {
	tr := sampleTree(t)
	entry, _ := tr.Get("a")
	entry.Size, entry.State = 99, model.Dirty
	if err := tr.Update(entry); err != nil {
		t.Fatal(err)
	}
	if got, _ := tr.Get("a"); got.Size != 99 || got.State != model.Dirty {
		t.Fatalf("update not applied: %+v", got)
	}
	entry.Name = "renamed"
	if err := tr.Update(entry); !errors.Is(err, ErrPlacementChanged) {
		t.Fatalf("update with new name: %v", err)
	}
}

func TestChildrenSortedAndAggregate(t *testing.T) {
	tr := sampleTree(t)
	mustInsert(t, tr, file("c", "docs", "0-first", 5, model.Dirty), file("d", "docs", "z-last", 7, model.Partial))
	var names []string
	for _, entry := range tr.Children("docs") {
		names = append(names, entry.Name)
	}
	if fmt.Sprint(names) != "[0-first a.txt sub z-last]" {
		t.Fatalf("children = %v", names)
	}
	total, local := tr.Aggregate("docs")
	if total != 42 || local != 15 {
		t.Fatalf("Aggregate = %d, %d; want 42, 15", total, local)
	}
}

func TestReturnedEntriesAreCopies(t *testing.T) {
	tr := sampleTree(t)
	entry, _ := tr.Get("a")
	entry.Name = "changed"
	if again, _ := tr.Get("a"); again.Name != "a.txt" {
		t.Fatal("caller mutated the tree through a returned entry")
	}
}
