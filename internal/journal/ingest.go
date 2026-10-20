package journal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
	"golang.org/x/sync/errgroup"
)

const ingestParallelism = 8

// ErrLogFileGone means a listed log file was deleted before it could be read, which happens
// when its device packed it in the meantime. The caller should list that folder again.
var ErrLogFileGone = errors.New("journal: listed log file no longer exists")

// Ingest downloads unseen commit and pack files and stores their commits as unapplied.
func (j *Journal) Ingest(ctx context.Context, files []model.RemoteFile, notFound func(error) bool) ([]Commit, error) {
	wanted, err := j.unseenLogFiles(ctx, files)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var fetched []fetchedCommit
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(ingestParallelism)
	for _, file := range wanted {
		group.Go(func() error {
			commits, err := j.fetchLogFile(groupCtx, file)
			if err != nil && notFound != nil && notFound(err) {
				return fmt.Errorf("%s: %w", file.Name, ErrLogFileGone)
			}
			mu.Lock()
			fetched = append(fetched, commits...)
			mu.Unlock()
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return j.storeRemoteCommits(ctx, fetched)
}

type fetchedCommit struct {
	commit Commit
	sealed []byte
}

func (j *Journal) unseenLogFiles(ctx context.Context, files []model.RemoteFile) ([]model.RemoteFile, error) {
	var wanted []model.RemoteFile
	err := j.Store.View(ctx, func(tx *index.Tx) error {
		for _, file := range files {
			if strings.HasSuffix(file.Name, packSuffix) {
				wanted = append(wanted, file)
				continue
			}
			if !strings.HasSuffix(file.Name, commitSuffix) {
				continue
			}
			_, err := tx.Commit(strings.TrimSuffix(file.Name, commitSuffix))
			if errors.Is(err, index.ErrNotFound) {
				wanted = append(wanted, file)
			} else if err != nil {
				return err
			}
		}
		return nil
	})
	return wanted, err
}

func (j *Journal) fetchLogFile(ctx context.Context, file model.RemoteFile) ([]fetchedCommit, error) {
	data, err := j.Remote.Download(ctx, file.ID)
	if err != nil {
		return nil, fmt.Errorf("download log file %s: %w", file.ID, err)
	}
	if strings.HasSuffix(file.Name, commitSuffix) {
		commit, err := j.openCommit(strings.TrimSuffix(file.Name, commitSuffix), data)
		return []fetchedCommit{{commit: commit, sealed: data}}, err
	}
	return j.unpack(file.Name, data)
}

// unpack re-seals each commit of a pack on its own so the index always holds single-commit blobs.
func (j *Journal) unpack(fileName string, data []byte) ([]fetchedCommit, error) {
	var out []fetchedCommit
	err := j.Keyring.Use(func(k *crypto.Keys) error {
		commits, err := j.Codec.OpenPack(fileName, data, k.Log)
		if err != nil {
			return err
		}
		for _, commit := range commits {
			sealed, err := j.Codec.SealCommit(commit, k.Log)
			if err != nil {
				return err
			}
			out = append(out, fetchedCommit{commit: commit, sealed: sealed})
		}
		return nil
	})
	return out, err
}

func (j *Journal) storeRemoteCommits(ctx context.Context, fetched []fetchedCommit) ([]Commit, error) {
	var stored []Commit
	err := j.Store.Update(ctx, func(tx *index.Tx) error {
		for _, f := range fetched {
			if _, err := tx.Commit(f.commit.ID); err == nil {
				continue
			} else if !errors.Is(err, index.ErrNotFound) {
				return err
			}
			own := f.commit.Device == j.config.Device
			record := index.CommitRecord{ID: f.commit.ID, Device: f.commit.Device, Clock: f.commit.Clock,
				Parents: f.commit.Parents, Sealed: f.sealed, Applied: own, Own: own, Referenced: own}
			if err := tx.PutCommit(record); err != nil {
				return err
			}
			stored = append(stored, f.commit)
		}
		return nil
	})
	for _, commit := range stored {
		j.Clock.Observe(commit.Clock)
	}
	return stored, err
}
