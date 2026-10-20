package drive

import (
	"context"
	"errors"
	"testing"
)

func TestChangesPagesThenReportsNewStartToken(t *testing.T) {
	_, client := newFakeAndClient(t)
	ctx := context.Background()
	token, err := client.StartPageToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parent := createTestFolder(t, client, "", "tree")
	for range 1200 {
		createTestFolder(t, client, parent.ID, "child")
	}
	removed := createTestFile(t, client, parent.ID, []byte("x"))
	if err := client.Delete(ctx, removed.ID); err != nil {
		t.Fatal(err)
	}

	first, err := client.Changes(ctx, token)
	if err != nil || len(first.Changes) != 1000 || first.NextPageToken == "" || first.NewStartPageToken != "" {
		t.Fatalf("first page: %d changes, next=%q start=%q, %v", len(first.Changes), first.NextPageToken, first.NewStartPageToken, err)
	}
	if change := first.Changes[0]; change.FileID != parent.ID || change.File == nil || !change.File.Folder || change.File.Properties["vid"] != "v1" {
		t.Fatalf("first change = %+v", change)
	}
	second, err := client.Changes(ctx, first.NextPageToken)
	if err != nil || len(second.Changes) != 203 || second.NewStartPageToken == "" {
		t.Fatalf("second page: %d changes, start=%q, %v", len(second.Changes), second.NewStartPageToken, err)
	}
	last := second.Changes[len(second.Changes)-1]
	if !last.Removed || last.FileID != removed.ID || last.File != nil {
		t.Fatalf("last change = %+v, want removal of %s", last, removed.ID)
	}
	idle, err := client.Changes(ctx, second.NewStartPageToken)
	if err != nil || len(idle.Changes) != 0 || idle.NewStartPageToken != second.NewStartPageToken {
		t.Fatalf("idle poll = %+v, %v", idle, err)
	}
}

func TestExpiredOrInvalidPageTokenIsReported(t *testing.T) {
	server, client := newFakeAndClient(t)
	ctx := context.Background()
	token, err := client.StartPageToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	createTestFolder(t, client, "", "folder")
	server.ExpirePageTokens()
	if _, err := client.Changes(ctx, token); !errors.Is(err, ErrPageTokenExpired) {
		t.Fatalf("expired token: %v, want ErrPageTokenExpired", err)
	}
	if _, err := client.Changes(ctx, "not-a-token"); !errors.Is(err, ErrPageTokenExpired) {
		t.Fatalf("invalid token: %v, want ErrPageTokenExpired", err)
	}
	fresh, err := client.StartPageToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Changes(ctx, fresh); err != nil {
		t.Fatalf("fresh token: %v", err)
	}
}
