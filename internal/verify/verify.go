// Package verify checks that Drive holds every body the index refers to: a shallow pass
// compares listings and checksums, a deep pass downloads and authenticates every body.
package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"

	"vault/internal/crypto"
	"vault/internal/index"
	"vault/internal/model"
)

// ErrRemoteNotFound is what Remote returns, wrapped, for a Drive file that does not exist.
var ErrRemoteNotFound = errors.New("verify: not found on Drive")

type Remote interface {
	ListChildren(ctx context.Context, folderID string) ([]model.RemoteFile, error)
	DownloadAll(ctx context.Context, driveID string, w io.Writer) error
}

type NameDecrypter interface {
	DecryptName(id model.ObjectID, encoded string) (string, error)
}

type Deps struct {
	Index   *index.Store
	Remote  Remote
	Names   NameDecrypter
	Keyring *crypto.Keyring
	Logger  *slog.Logger
}

type Config struct {
	TreeFolderID  string
	TrashFolderID string
	// Listings bounds parallel folder listings, Downloads parallel deep body checks.
	Listings  int
	Downloads int
	// TempDir holds one downloaded ciphertext per deep check while it is verified.
	TempDir string
}

type ProblemKind string

const (
	MissingOnDrive       ProblemKind = "missing on Drive"
	ChecksumMismatch     ProblemKind = "checksum mismatch"
	FailedAuthentication ProblemKind = "failed authentication"
	IdentityMismatch     ProblemKind = "body identity mismatch"
)

type Problem struct {
	ID   model.ObjectID
	Path string
	Kind ProblemKind
	// Trashed marks a problem with an item in trash/ rather than in the tree.
	Trashed bool
}

type Report struct {
	Deep            bool
	FoldersListed   int
	FilesChecked    int
	TrashChecked    int
	BodiesVerified  int
	BytesDownloaded int64
	Problems        []Problem
}

func (r *Report) OK() bool {
	return len(r.Problems) == 0
}

type Verifier struct {
	deps   Deps
	config Config
}

func New(deps Deps, config Config) *Verifier {
	if config.Listings < 1 {
		config.Listings = 8
	}
	if config.Downloads < 1 {
		config.Downloads = 4
	}
	if config.TempDir == "" {
		config.TempDir = os.TempDir()
	}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.DiscardHandler)
	}
	return &Verifier{deps: deps, config: config}
}

// Verify runs the shallow pass and, with deep, authenticates every body that pass found on
// Drive. Problems with individual objects go into the report; the error is for a verify
// that could not finish.
func (v *Verifier) Verify(ctx context.Context, deep bool) (Report, error) {
	snapshot, err := v.loadSnapshot(ctx)
	if err != nil {
		return Report{}, err
	}
	report := Report{Deep: deep}
	present, err := v.checkListings(ctx, snapshot, &report)
	if err != nil {
		return Report{}, err
	}
	if deep {
		if err := v.checkBodies(ctx, present, &report); err != nil {
			return Report{}, err
		}
	}
	slices.SortFunc(report.Problems, func(a, b Problem) int {
		return strings.Compare(a.Path+"\x00"+string(a.ID), b.Path+"\x00"+string(b.ID))
	})
	v.deps.Logger.Info("verify finished", "deep", deep, "files", report.FilesChecked, "trash", report.TrashChecked,
		"bodies", report.BodiesVerified, "problems", len(report.Problems))
	return report, nil
}

// bodyCheck is one Drive body with what the index expects of it.
type bodyCheck struct {
	id         model.ObjectID
	path       string
	trashed    bool
	driveID    string
	bodyMD5    string
	bodyID     []byte
	contentMAC []byte
}

func (c *bodyCheck) problem(kind ProblemKind) Problem {
	return Problem{ID: c.id, Path: c.path, Kind: kind, Trashed: c.trashed}
}

func wrapRemote(operation string, err error) error {
	return fmt.Errorf("verify: %s: %w", operation, err)
}
