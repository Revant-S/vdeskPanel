package remote

import (
	"context"
	"fmt"
	"io"

	"github.com/cosmoboy/vault/internal/cache"
	"github.com/cosmoboy/vault/internal/drive"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
)

// Bodies serves the cache, mapping an object to the Drive file of its current version.
type Bodies struct {
	Client *drive.Client
	Index  *index.Store
}

var _ cache.SegmentFetcher = Bodies{}

func (b Bodies) FetchRange(ctx context.Context, id model.ObjectID, offset, length int64, w io.WriterAt) error {
	var driveID string
	err := b.Index.View(ctx, func(tx *index.Tx) error {
		object, err := tx.Object(id)
		driveID = object.DriveID
		return err
	})
	if err != nil {
		return err
	}
	if driveID == "" {
		return fmt.Errorf("object %s has no remote body", id)
	}
	return b.Client.DownloadRange(ctx, driveID, offset, length, w)
}
