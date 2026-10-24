package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
	"vault/internal/crypto"
	"vault/internal/index"
	"vault/internal/model"
)

const (
	ingestParallelism = 8
	ingestedPacksKey  = "journal.ingested_packs"
)

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
	var packs []string
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(ingestParallelism)
	for _, file := range wanted {
		group.Go(func() error {
			commits, err := j.fetchLogFile(groupCtx, file)
			if err != nil && notFound != nil && notFound(err) {
				return fmt.Errorf("%s: %w", file.Name, ErrLogFileGone)
			}
			if err != nil {
				return err
			}
			mu.Lock()
			fetched = append(fetched, commits...)
			if strings.HasSuffix(file.Name, packSuffix) {
				packs = append(packs, file.ID)
			}
			mu.Unlock()
			return nil
		})
	}
	fetchErr := group.Wait()
	// What arrived is kept even when another file failed or the catch-up deadline hit: storing
	// is idempotent by commit ID and nothing is applied until a poll completes, so the next
	// poll only fetches what is still missing.
	stored, err := j.storeRemoteCommits(context.WithoutCancel(ctx), fetched, packs)
	return stored, errors.Join(fetchErr, err)
}

type fetchedCommit struct {
	commit Commit
	sealed []byte
}

func (j *Journal) unseenLogFiles(ctx context.Context, files []model.RemoteFile) ([]model.RemoteFile, error) {
	var wanted []model.RemoteFile
	err := j.Store.View(ctx, func(tx *index.Tx) error {
		ingested, err := ingestedPacks(tx)
		if err != nil {
			return err
		}
		for _, file := range files {
			if strings.HasSuffix(file.Name, packSuffix) {
				if !ingested[file.ID] {
					wanted = append(wanted, file)
				}
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

func (j *Journal) storeRemoteCommits(ctx context.Context, fetched []fetchedCommit, packs []string) ([]Commit, error) {
	if len(fetched) == 0 && len(packs) == 0 {
		return nil, nil
	}
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
		return rememberPacks(tx, packs)
	})
	for _, commit := range stored {
		j.Clock.Observe(commit.Clock)
	}
	return stored, err
}

// Packs never change once written, so a pack ingested once is not downloaded again.
func ingestedPacks(tx *index.Tx) (map[string]bool, error) {
	seen := map[string]bool{}
	data, found, err := tx.KV(ingestedPacksKey)
	if err != nil || !found {
		return seen, err
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, err
	}
	for _, id := range ids {
		seen[id] = true
	}
	return seen, nil
}

func rememberPacks(tx *index.Tx, packs []string) error {
	if len(packs) == 0 {
		return nil
	}
	seen, err := ingestedPacks(tx)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(seen)+len(packs))
	for id := range seen {
		ids = append(ids, id)
	}
	for _, id := range packs {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return tx.SetKV(ingestedPacksKey, data)
}
