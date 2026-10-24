package orphans

import (
	"context"
	"fmt"
	"testing"
	"time"

	"vault/internal/model"
)

type memoryRemote struct {
	files   []model.RemoteFile
	deleted []string
}

func (m *memoryRemote) ListChildren(_ context.Context, folderID string) ([]model.RemoteFile, error) {
	var out []model.RemoteFile
	for _, file := range m.files {
		if file.Parents[0] == folderID {
			out = append(out, file)
		}
	}
	return out, nil
}

func (m *memoryRemote) Delete(_ context.Context, driveID string) error {
	m.deleted = append(m.deleted, driveID)
	return nil
}

func TestCollectDeletesOnlyOldUnreferencedFilesOfThisVault(t *testing.T) {
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	old, young := now.Add(-40*24*time.Hour), now.Add(-time.Hour)
	file := func(id, parent string, created time.Time, vid string, folder bool) model.RemoteFile {
		return model.RemoteFile{ID: id, Parents: []string{parent}, CreatedTime: created, Folder: folder, Properties: map[string]string{"vid": vid}}
	}
	remote := &memoryRemote{files: []model.RemoteFile{
		file("live-folder", "tree", old, "v", true),
		file("live-file", "live-folder", old, "v", false),
		file("orphan-deep", "live-folder", old, "v", false),
		file("orphan-young", "live-folder", young, "v", false),
		file("orphan-folder", "tree", old, "v", true),
		file("inside-orphan-folder", "orphan-folder", old, "v", false),
		file("other-vault", "tree", old, "w", false),
		file("trash-item", "trash", old, "v", false),
		file("trash-orphan", "trash", old, "v", false),
		file("applied-during-walk", "tree", old, "v", false),
	}}
	calls := 0
	references := func(context.Context) (map[string]bool, error) {
		calls++
		refs := map[string]bool{"live-folder": true, "live-file": true, "trash-item": true}
		if calls > 1 {
			refs["applied-during-walk"] = true
		}
		return refs, nil
	}
	collector := New(Deps{Remote: remote, References: references, Now: func() time.Time { return now }},
		Config{VaultID: "v", TreeFolderID: "tree", TrashFolderID: "trash", MinAge: 30 * 24 * time.Hour})

	result, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"orphan-deep": true, "orphan-folder": true, "trash-orphan": true}
	if len(remote.deleted) != len(want) || result.Deleted != len(want) || result.Found != len(want)+1 {
		t.Fatalf("deleted %v, result %+v", remote.deleted, result)
	}
	for _, id := range remote.deleted {
		if !want[id] {
			t.Fatalf("deleted %s", id)
		}
	}
}

func TestCollectRefusesWhenMostFilesLookOrphaned(t *testing.T) {
	now := time.Now()
	remote := &memoryRemote{}
	for i := range 60 {
		remote.files = append(remote.files, model.RemoteFile{ID: fmt.Sprint(i), Parents: []string{"tree"},
			CreatedTime: now.Add(-365 * 24 * time.Hour), Properties: map[string]string{"vid": "v"}})
	}
	empty := func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }
	collector := New(Deps{Remote: remote, References: empty, Now: func() time.Time { return now }},
		Config{VaultID: "v", TreeFolderID: "tree", TrashFolderID: "trash", MinAge: 30 * 24 * time.Hour})
	result, err := collector.Collect(context.Background())
	if err != nil || !result.Refused || len(remote.deleted) != 0 {
		t.Fatalf("result %+v, deleted %v, err %v", result, remote.deleted, err)
	}
}
