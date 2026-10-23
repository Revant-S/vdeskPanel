package remote

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cosmoboy/vault/internal/drive"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/verify"
)

// Verify serves the backup health check.
type Verify struct {
	Client *drive.Client
}

var _ verify.Remote = (*Verify)(nil)

func (v *Verify) ListChildren(ctx context.Context, folderID string) ([]model.RemoteFile, error) {
	files, err := v.Client.ListChildren(ctx, folderID)
	return files, translateForVerify(err)
}

func (v *Verify) DownloadAll(ctx context.Context, driveID string, w io.Writer) error {
	return translateForVerify(v.Client.DownloadAll(ctx, driveID, w))
}

func translateForVerify(err error) error {
	if errors.Is(err, drive.ErrNotFound) {
		return fmt.Errorf("%w: %w", verify.ErrRemoteNotFound, err)
	}
	return err
}
