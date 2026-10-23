package syncer

import (
	"encoding/json"
	"testing"

	"vault/internal/index"
	"vault/internal/model"
)

type opBuilder struct {
	ops []index.PendingOp
}

func (b *opBuilder) add(op model.LocalOp) *opBuilder {
	payload, _ := json.Marshal(op)
	seq := int64(len(b.ops) + 1)
	b.ops = append(b.ops, index.PendingOp{Seq: seq, ObjectID: op.ObjectID, Kind: string(op.Kind), Payload: payload,
		Stamp: model.Stamp{WallMillis: seq, Device: "d-a"}})
	return b
}

func syncedSet(ids ...model.ObjectID) RemoteFacts {
	set := map[model.ObjectID]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return func(id model.ObjectID) bool { return set[id] }
}

func reduce(t *testing.T, b *opBuilder, synced RemoteFacts) []NetOp {
	t.Helper()
	net, err := ReduceToNetEffect(b.ops, synced)
	if err != nil {
		t.Fatal(err)
	}
	return net
}

func kinds(net []NetOp) []string {
	var out []string
	for _, op := range net {
		out = append(out, string(op.Kind)+":"+string(op.ObjectID))
	}
	return out
}

func expectKinds(t *testing.T, net []NetOp, want ...string) {
	t.Helper()
	got := kinds(net)
	if len(got) != len(want) {
		t.Fatalf("net = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("net = %v, want %v", got, want)
		}
	}
}

func TestCreateThenDeleteNeverReachesDrive(t *testing.T) {
	b := (&opBuilder{}).
		add(model.LocalOp{Kind: model.LocalCreate, ObjectID: "tmp", Path: "/.goutputstream-1"}).
		add(model.LocalOp{Kind: model.LocalModify, ObjectID: "tmp", Path: "/.goutputstream-1"}).
		add(model.LocalOp{Kind: model.LocalDelete, ObjectID: "tmp", Path: "/.goutputstream-1"})
	expectKinds(t, reduce(t, b, syncedSet()), "drop_local:tmp")
}

func TestCreateRenameModifyIsOneCreateAtFinalPlace(t *testing.T) {
	b := (&opBuilder{}).
		add(model.LocalOp{Kind: model.LocalCreate, ObjectID: "a", Path: "/a"}).
		add(model.LocalOp{Kind: model.LocalMove, ObjectID: "a", FromPath: "/a", Path: "/Docs/b"}).
		add(model.LocalOp{Kind: model.LocalModify, ObjectID: "a", Path: "/Docs/b"}).
		add(model.LocalOp{Kind: model.LocalAttr, ObjectID: "a", Path: "/Docs/b"})
	net := reduce(t, b, syncedSet())
	expectKinds(t, net, "create:a")
	if net[0].Path != "/Docs/b" || len(net[0].Seqs) != 4 {
		t.Fatalf("create = %+v", net[0])
	}
}

func TestMoveThereAndBackIsANoop(t *testing.T) {
	b := (&opBuilder{}).
		add(model.LocalOp{Kind: model.LocalMove, ObjectID: "a", FromPath: "/a", Path: "/b"}).
		add(model.LocalOp{Kind: model.LocalMove, ObjectID: "a", FromPath: "/b", Path: "/a"})
	expectKinds(t, reduce(t, b, syncedSet("a")), "noop:a")
}

func TestMoveAndAttrOfSyncedObjectStaySeparate(t *testing.T) {
	b := (&opBuilder{}).
		add(model.LocalOp{Kind: model.LocalMove, ObjectID: "a", FromPath: "/a", Path: "/b"}).
		add(model.LocalOp{Kind: model.LocalAttr, ObjectID: "a", Path: "/b"})
	net := reduce(t, b, syncedSet("a"))
	expectKinds(t, net, "move:a", "attr:a")
	if net[0].FromPath != "/a" || net[0].Path != "/b" {
		t.Fatalf("move = %+v", net[0])
	}
}

func TestModifyCarriesMoveAndDeleteOfModifiedFileTrashesLatest(t *testing.T) {
	b := (&opBuilder{}).
		add(model.LocalOp{Kind: model.LocalMove, ObjectID: "a", FromPath: "/a", Path: "/b"}).
		add(model.LocalOp{Kind: model.LocalModify, ObjectID: "a", Path: "/b"}).
		add(model.LocalOp{Kind: model.LocalModify, ObjectID: "c", Path: "/c"}).
		add(model.LocalOp{Kind: model.LocalDelete, ObjectID: "c", Path: "/c", Reason: model.ReasonDeleted})
	net := reduce(t, b, syncedSet("a", "c"))
	expectKinds(t, net, "modify:a", "delete:c")
	if !net[0].Moved || net[0].FromPath != "/a" || !net[1].TrashLatest {
		t.Fatalf("net = %+v", net)
	}
}

func TestRenameOverIsModifyPlusAbsorb(t *testing.T) {
	b := (&opBuilder{}).
		add(model.LocalOp{Kind: model.LocalCreate, ObjectID: "tmp", Path: "/.doc.tmp"}).
		add(model.LocalOp{Kind: model.LocalModify, ObjectID: "tmp", Path: "/.doc.tmp"}).
		add(model.LocalOp{Kind: model.LocalModify, ObjectID: "doc", Path: "/doc", ReplacedFrom: "tmp"}).
		add(model.LocalOp{Kind: model.LocalAbsorb, ObjectID: "tmp", ReplacedFrom: "doc"})
	expectKinds(t, reduce(t, b, syncedSet("doc")), "drop_local:tmp", "modify:doc")
	b2 := (&opBuilder{}).
		add(model.LocalOp{Kind: model.LocalModify, ObjectID: "doc", Path: "/doc", ReplacedFrom: "old"}).
		add(model.LocalOp{Kind: model.LocalAbsorb, ObjectID: "old", ReplacedFrom: "doc"})
	expectKinds(t, reduce(t, b2, syncedSet("doc", "old")), "modify:doc", "absorb:old")
}

func TestVimSaveAdoptsTheOriginalIdentity(t *testing.T) {
	b := (&opBuilder{}).
		add(model.LocalOp{Kind: model.LocalCreate, ObjectID: "swp", Path: "/.notes.md.swp"}).
		add(model.LocalOp{Kind: model.LocalMove, ObjectID: "orig", FromPath: "/notes.md", Path: "/notes.md~"}).
		add(model.LocalOp{Kind: model.LocalCreate, ObjectID: "new", Path: "/notes.md"}).
		add(model.LocalOp{Kind: model.LocalModify, ObjectID: "new", Path: "/notes.md"}).
		add(model.LocalOp{Kind: model.LocalDelete, ObjectID: "orig", Path: "/notes.md~", Reason: model.ReasonDeleted}).
		add(model.LocalOp{Kind: model.LocalDelete, ObjectID: "swp", Path: "/.notes.md.swp", Reason: model.ReasonDeleted})
	net := reduce(t, b, syncedSet("orig"))
	expectKinds(t, net, "drop_local:swp", "modify:orig")
	if net[1].AdoptFrom != "new" || net[1].Path != "/notes.md" || len(net[1].Seqs) != 4 {
		t.Fatalf("adoption = %+v", net[1])
	}
}

func TestMovedOutFileIsNotAdopted(t *testing.T) {
	b := (&opBuilder{}).
		add(model.LocalOp{Kind: model.LocalDelete, ObjectID: "orig", Path: "/clip.mp4", Reason: model.ReasonMovedOut}).
		add(model.LocalOp{Kind: model.LocalCreate, ObjectID: "back", Path: "/clip.mp4"})
	expectKinds(t, reduce(t, b, syncedSet("orig")), "delete:orig", "create:back")
}

func TestFolderDeleteSubsumesChildrenAndCountsFiles(t *testing.T) {
	b := &opBuilder{}
	for _, id := range []model.ObjectID{"p1", "p2", "p3"} {
		b.add(model.LocalOp{Kind: model.LocalDelete, ObjectID: id, Path: "/Photos/2024/" + string(id), Reason: model.ReasonMovedOut})
	}
	b.add(model.LocalOp{Kind: model.LocalDelete, ObjectID: "y2024", Folder: true, Path: "/Photos/2024"}).
		add(model.LocalOp{Kind: model.LocalDelete, ObjectID: "top", Path: "/Photos/top.jpg", Reason: model.ReasonMovedOut}).
		add(model.LocalOp{Kind: model.LocalDelete, ObjectID: "photos", Folder: true, Path: "/Photos"}).
		add(model.LocalOp{Kind: model.LocalDelete, ObjectID: "other", Path: "/PhotosOld/x", Reason: model.ReasonDeleted})
	net := reduce(t, b, syncedSet("p1", "p2", "p3", "y2024", "top", "photos", "other"))
	var folder NetOp
	subsumed := 0
	for _, op := range net {
		if op.ObjectID == "photos" {
			folder = op
		}
		if op.SubsumedBy == "photos" {
			subsumed++
		}
		if op.ObjectID == "other" && op.SubsumedBy != "" {
			t.Fatal("sibling with a shared prefix was subsumed")
		}
	}
	if subsumed != 5 || folder.FileCount != 4 || folder.Reason != model.ReasonMovedOut || folder.SubsumedBy != "" {
		t.Fatalf("folder = %+v, subsumed = %d", folder, subsumed)
	}
}

func TestFolderDeleteWithAnOrdinaryDeleteIsDeleted(t *testing.T) {
	b := (&opBuilder{}).
		add(model.LocalOp{Kind: model.LocalDelete, ObjectID: "f", Path: "/D/f", Reason: model.ReasonDeleted}).
		add(model.LocalOp{Kind: model.LocalDelete, ObjectID: "d", Folder: true, Path: "/D"})
	net := reduce(t, b, syncedSet("f", "d"))
	if net[1].Reason == model.ReasonMovedOut {
		t.Fatal("folder labelled moved out though a child was a plain delete")
	}
}
