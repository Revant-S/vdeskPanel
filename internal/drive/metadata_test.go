package drive

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/drive/drivefake"
)

func TestGenerateIDsThenCreateWithIDAndConflict(t *testing.T) {
	server, client := newFakeAndClient(t)
	ctx := context.Background()
	ids, err := client.GenerateIDs(ctx, 1500)
	if err != nil {
		t.Fatal(err)
	}
	unique := map[string]bool{}
	for _, id := range ids {
		unique[id] = true
	}
	if len(ids) != 1500 || len(unique) != 1500 {
		t.Fatalf("got %d ids, %d unique", len(ids), len(unique))
	}
	folder, err := client.CreateFolder(ctx, NewFile{ID: ids[0], Name: "tree"})
	if err != nil || folder.ID != ids[0] || !folder.Folder {
		t.Fatalf("create with id = %+v, %v", folder, err)
	}
	_, err = client.CreateFolder(ctx, NewFile{ID: ids[0], Name: "tree"})
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate id: %v, want ErrAlreadyExists", err)
	}
	if _, err := client.CreateFolder(ctx, NewFile{ID: "never-generated", Name: "x"}); err == nil {
		t.Fatal("create with an id Drive never issued succeeded")
	}
	if _, _, ok := server.File(ids[0]); !ok {
		t.Fatal("fake has no file under the pre-generated id")
	}
}

func TestEnsureFolderReusesExistingFolder(t *testing.T) {
	server, client := newFakeAndClient(t)
	first, err := client.EnsureFolder(context.Background(), NewFile{Name: "Vault"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.EnsureFolder(context.Background(), NewFile{Name: "Vault"})
	if err != nil || second.ID != first.ID {
		t.Fatalf("second ensure = %s, %v; want %s", second.ID, err, first.ID)
	}
	creates := 0
	for _, record := range server.Requests() {
		if record.Method == http.MethodPost {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("%d creates, want 1", creates)
	}
}

func TestCreateSmallFileReturnsEveryMaskedField(t *testing.T) {
	server, client := newFakeAndClient(t)
	ctx := context.Background()
	parent := createTestFolder(t, client, "", "tree")
	content := randomContent(t, 70000)
	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	properties := map[string]string{"vid": "v1", "oid": "o1"}
	file, err := client.CreateSmallFile(ctx, NewFile{Name: "enc", ParentID: parent.ID, ModifiedTime: modified, Properties: properties}, bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Get(ctx, file.ID)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(content)
	switch {
	case got.MD5 != hex.EncodeToString(sum[:]):
		t.Fatalf("md5 %q does not match content", got.MD5)
	case got.Size != int64(len(content)) || got.Name != "enc" || got.Folder || got.Trashed:
		t.Fatalf("unexpected file %+v", got)
	case len(got.Parents) != 1 || got.Parents[0] != parent.ID:
		t.Fatalf("parents %v, want [%s]", got.Parents, parent.ID)
	case got.Properties["vid"] != "v1" || got.Properties["oid"] != "o1":
		t.Fatalf("properties %v", got.Properties)
	case !got.ModifiedTime.Equal(modified) || got.CreatedTime.IsZero():
		t.Fatalf("times modified=%v created=%v", got.ModifiedTime, got.CreatedTime)
	}
	if _, stored, _ := server.File(file.ID); !bytes.Equal(stored, content) {
		t.Fatal("stored content differs")
	}
	big := make([]byte, MaxSmallFileSize+1)
	if _, err := client.CreateSmallFile(ctx, NewFile{Name: "big"}, bytes.NewReader(big), int64(len(big))); err == nil {
		t.Fatal("small-file create accepted more than 5 MiB")
	}
}

func TestCopyIsServerSideWithSameChecksum(t *testing.T) {
	server, client := newFakeAndClient(t)
	ctx := context.Background()
	source := createTestFile(t, client, "", randomContent(t, 1000))
	ids, _ := client.GenerateIDs(ctx, 1)
	uploadsBefore := countRequests(server, drivefake.KindUpload)
	copied, err := client.Copy(ctx, source.ID, NewFile{ID: ids[0], Name: "copy", Properties: map[string]string{"oid": "o2"}})
	if err != nil {
		t.Fatal(err)
	}
	if copied.ID != ids[0] || copied.MD5 != source.MD5 || copied.Properties["oid"] != "o2" {
		t.Fatalf("copy = %+v", copied)
	}
	if countRequests(server, drivefake.KindUpload) != uploadsBefore {
		t.Fatal("copy uploaded content")
	}
}

func TestUpdateRenamesMovesAndRetagsInOneCall(t *testing.T) {
	server, client := newFakeAndClient(t)
	ctx := context.Background()
	from := createTestFolder(t, client, "", "from")
	to := createTestFolder(t, client, "", "to")
	file := createTestFile(t, client, from.ID, []byte("x"))
	before := len(server.Requests())
	modified := time.Date(2025, 5, 5, 5, 5, 5, 0, time.UTC)
	updated, err := client.Update(ctx, file.ID, MetadataChange{
		Name: "renamed", AddParents: []string{to.ID}, RemoveParents: []string{from.ID},
		Properties: map[string]string{"oid": "o9"}, ModifiedTime: modified,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls := len(server.Requests()) - before; calls != 1 {
		t.Fatalf("update took %d calls, want 1", calls)
	}
	if updated.Name != "renamed" || len(updated.Parents) != 1 || updated.Parents[0] != to.ID ||
		updated.Properties["oid"] != "o9" || !updated.ModifiedTime.Equal(modified) || updated.MD5 != file.MD5 {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestDeleteTreatsMissingAsSuccess(t *testing.T) {
	server, client := newFakeAndClient(t)
	ctx := context.Background()
	folder := createTestFolder(t, client, "", "trash")
	child := createTestFile(t, client, folder.ID, []byte("x"))
	if err := client.Delete(ctx, folder.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := server.File(child.ID); ok {
		t.Fatal("deleting a folder left its child")
	}
	if err := client.Delete(ctx, folder.ID); err != nil {
		t.Fatalf("second delete: %v, want nil", err)
	}
	if _, err := client.Get(ctx, folder.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v, want ErrNotFound", err)
	}
}

func TestListChildrenFollowsPages(t *testing.T) {
	server, client := newFakeAndClient(t)
	parent := createTestFolder(t, client, "", "tree")
	createTestFolder(t, client, "", "elsewhere")
	for range 1203 {
		createTestFolder(t, client, parent.ID, "child")
	}
	children, err := client.ListChildren(context.Background(), parent.ID)
	if err != nil || len(children) != 1203 {
		t.Fatalf("listed %d children, %v; want 1203", len(children), err)
	}
	if lists := countRequests(server, drivefake.KindList); lists != 2 {
		t.Fatalf("%d list requests, want 2 pages of 1000", lists)
	}
}

func TestFindByPropertiesMatchesEveryPair(t *testing.T) {
	_, client := newFakeAndClient(t)
	ctx := context.Background()
	parent := createTestFolder(t, client, "", "tree")
	for _, oid := range []string{"a", "b", "it's"} {
		if _, err := client.CreateFolder(ctx, NewFile{Name: oid, ParentID: parent.ID, Properties: map[string]string{"vid": "v1", "oid": oid}}); err != nil {
			t.Fatal(err)
		}
	}
	found, err := client.FindByProperties(ctx, parent.ID, map[string]string{"vid": "v1", "oid": "it's"})
	if err != nil || len(found) != 1 || found[0].Name != "it's" {
		t.Fatalf("found %+v, %v", found, err)
	}
	everywhere, err := client.FindByProperties(ctx, "", map[string]string{"vid": "v1"})
	if err != nil || len(everywhere) != 4 {
		t.Fatalf("found %d across the drive, %v; want 4", len(everywhere), err)
	}
}
