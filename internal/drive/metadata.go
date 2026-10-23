package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	drive "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"

	"vault/internal/model"
)

const maxGeneratedIDsPerCall = 1000

func (c *Client) GenerateIDs(ctx context.Context, count int) ([]string, error) {
	ids := make([]string, 0, count)
	for len(ids) < count {
		batch := min(count-len(ids), maxGeneratedIDsPerCall)
		generated, err := callWithRetry(ctx, c, c.budget.reads, "generate ids", func(ctx context.Context) (*drive.GeneratedIds, error) {
			return c.service.Files.GenerateIds().Count(int64(batch)).Space("drive").Type("files").Context(ctx).Do()
		})
		if err != nil {
			return nil, err
		}
		ids = append(ids, generated.Ids...)
	}
	return ids, nil
}

func (c *Client) CreateFolder(ctx context.Context, folder NewFile) (model.RemoteFile, error) {
	created, err := callWithRetry(ctx, c, c.budget.metadataWrites, "create folder", func(ctx context.Context) (*drive.File, error) {
		return c.service.Files.Create(folder.toDrive(FolderMimeType)).Fields(fileFields).Context(ctx).Do()
	})
	if err != nil {
		return model.RemoteFile{}, err
	}
	return toRemoteFile(created), nil
}

// EnsureFolder returns the folder named folder.Name under folder.ParentID, creating it when
// it does not exist yet.
func (c *Client) EnsureFolder(ctx context.Context, folder NewFile) (model.RemoteFile, error) {
	query := queryFor(folder.ParentID, nameClause(folder.Name), mimeTypeClause(FolderMimeType))
	existing, err := c.listAll(ctx, query, "find folder")
	if err != nil {
		return model.RemoteFile{}, err
	}
	if len(existing) > 0 {
		return existing[0], nil
	}
	created, err := c.CreateFolder(ctx, folder)
	if errors.Is(err, ErrAlreadyExists) && folder.ID != "" {
		return c.Get(ctx, folder.ID)
	}
	return created, err
}

func (c *Client) CreateSmallFile(ctx context.Context, file NewFile, content io.ReaderAt, size int64) (model.RemoteFile, error) {
	if size > MaxSmallFileSize {
		return model.RemoteFile{}, fmt.Errorf("drive: small file of %d bytes exceeds %d", size, MaxSmallFileSize)
	}
	created, err := callWithRetry(ctx, c, c.budget.metadataWrites, "create file", func(ctx context.Context) (*drive.File, error) {
		body := io.NewSectionReader(content, 0, size)
		return c.service.Files.Create(file.toDrive(contentMimeType)).
			Media(body, googleapi.ContentType(contentMimeType), googleapi.ChunkSize(0)).
			Fields(fileFields).Context(ctx).Do()
	})
	if err != nil {
		return model.RemoteFile{}, err
	}
	return toRemoteFile(created), nil
}

// Copy makes a server-side copy of sourceID as target; Drive copies the bytes, so the
// copy's md5Checksum equals the source's.
func (c *Client) Copy(ctx context.Context, sourceID string, target NewFile) (model.RemoteFile, error) {
	copied, err := callWithRetry(ctx, c, c.budget.metadataWrites, "copy file", func(ctx context.Context) (*drive.File, error) {
		return c.service.Files.Copy(sourceID, target.toDrive("")).Fields(fileFields).Context(ctx).Do()
	})
	if err != nil {
		return model.RemoteFile{}, err
	}
	return toRemoteFile(copied), nil
}

// MetadataChange is applied in a single files.update call. Zero fields are left unchanged;
// Properties are merged into the existing appProperties.
type MetadataChange struct {
	Name          string
	AddParents    []string
	RemoveParents []string
	Properties    map[string]string
	ModifiedTime  time.Time
}

func (c *Client) Update(ctx context.Context, id string, change MetadataChange) (model.RemoteFile, error) {
	patch := &drive.File{Name: change.Name, AppProperties: change.Properties}
	if !change.ModifiedTime.IsZero() {
		patch.ModifiedTime = formatTime(change.ModifiedTime)
	}
	updated, err := callWithRetry(ctx, c, c.budget.metadataWrites, "update file", func(ctx context.Context) (*drive.File, error) {
		call := c.service.Files.Update(id, patch).Fields(fileFields).Context(ctx)
		if len(change.AddParents) > 0 {
			call = call.AddParents(joinIDs(change.AddParents))
		}
		if len(change.RemoveParents) > 0 {
			call = call.RemoveParents(joinIDs(change.RemoveParents))
		}
		return call.Do()
	})
	if err != nil {
		return model.RemoteFile{}, err
	}
	return toRemoteFile(updated), nil
}

// Delete removes id permanently. An object that is already gone counts as deleted, which
// keeps replayed deletes idempotent.
func (c *Client) Delete(ctx context.Context, id string) error {
	_, err := callWithRetry(ctx, c, c.budget.metadataWrites, "delete file", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, c.service.Files.Delete(id).Context(ctx).Do()
	})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (c *Client) Get(ctx context.Context, id string) (model.RemoteFile, error) {
	file, err := callWithRetry(ctx, c, c.budget.reads, "get file", func(ctx context.Context) (*drive.File, error) {
		return c.service.Files.Get(id).Fields(fileFields).Context(ctx).Do()
	})
	if err != nil {
		return model.RemoteFile{}, err
	}
	return toRemoteFile(file), nil
}
