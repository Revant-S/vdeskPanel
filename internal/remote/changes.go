package remote

import (
	"context"
	"errors"
	"fmt"

	"github.com/cosmoboy/vault/internal/drive"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/syncer"
)

// Changes serves the incoming poller.
type Changes struct {
	Client *drive.Client
}

var _ syncer.ChangeSource = Changes{}

func (c Changes) StartPageToken(ctx context.Context) (string, error) {
	return c.Client.StartPageToken(ctx)
}

func (c Changes) Changes(ctx context.Context, token string) (model.ChangesPage, error) {
	page, err := c.Client.Changes(ctx, token)
	if errors.Is(err, drive.ErrPageTokenExpired) {
		return page, fmt.Errorf("%w: %w", syncer.ErrChangesExpired, err)
	}
	return page, err
}

func (c Changes) ListChildren(ctx context.Context, folderID string) ([]model.RemoteFile, error) {
	return c.Client.ListChildren(ctx, folderID)
}
