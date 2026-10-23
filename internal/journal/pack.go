package journal

import (
	"context"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"

	"vault/internal/index"
)

func incrementSingles(tx *index.Tx) error {
	count, err := readSingles(tx)
	if err != nil {
		return err
	}
	return tx.SetKV(singlesKey, binary.BigEndian.AppendUint64(nil, uint64(count+1)))
}

func readSingles(tx *index.Tx) (int, error) {
	value, ok, err := tx.KV(singlesKey)
	if err != nil || !ok {
		return 0, err
	}
	return int(binary.BigEndian.Uint64(value)), nil
}

// PackIfDue folds this device's single commit files into one pack once there are enough.
// The pack is written before the singles are deleted, and readers dedupe by commit ID, so a
// crash in between only leaves duplicates.
func (j *Journal) PackIfDue(ctx context.Context) error {
	var singles int
	if err := j.Store.View(ctx, func(tx *index.Tx) error {
		var err error
		singles, err = readSingles(tx)
		return err
	}); err != nil || singles < j.config.PackEvery {
		return err
	}
	files, err := j.Remote.ListFolder(ctx, j.config.OwnLogFolderID)
	if err != nil {
		return fmt.Errorf("list own log folder: %w", err)
	}
	var commits []Commit
	var driveIDs []string
	for _, file := range files {
		if !strings.HasSuffix(file.Name, commitSuffix) {
			continue
		}
		record, err := j.storedCommit(ctx, strings.TrimSuffix(file.Name, commitSuffix))
		if err != nil {
			return err
		}
		commit, err := j.OpenRecord(record)
		if err != nil {
			return err
		}
		commits, driveIDs = append(commits, commit), append(driveIDs, file.ID)
	}
	if len(commits) == 0 {
		return j.resetSingles(ctx)
	}
	if err := j.uploadPack(ctx, commits); err != nil {
		return err
	}
	for _, driveID := range driveIDs {
		if err := j.Remote.Delete(ctx, driveID); err != nil {
			return fmt.Errorf("delete packed commit: %w", err)
		}
	}
	return j.resetSingles(ctx)
}

func (j *Journal) uploadPack(ctx context.Context, commits []Commit) error {
	sort.Slice(commits, func(a, b int) bool { return commits[a].Clock.Compare(commits[b].Clock) < 0 })
	name := fmt.Sprintf("pack-%s-%s%s", commits[0].Clock, commits[len(commits)-1].Clock, packSuffix)
	sealed, err := j.seal(func(key cipher.AEAD) ([]byte, error) { return j.Codec.SealPack(name, commits, key) })
	if err != nil {
		return err
	}
	driveID, err := j.Remote.GenerateID(ctx)
	if err != nil {
		return err
	}
	file := LogFile{DriveID: driveID, FolderID: j.config.OwnLogFolderID, Name: name,
		Properties: map[string]string{"vid": j.config.VaultID, "kind": "pack"}, Data: sealed}
	if _, err := j.Remote.CreateFile(ctx, file); err != nil {
		return fmt.Errorf("upload pack: %w", err)
	}
	return nil
}

func (j *Journal) resetSingles(ctx context.Context) error {
	return j.Store.Update(ctx, func(tx *index.Tx) error {
		return tx.SetKV(singlesKey, binary.BigEndian.AppendUint64(nil, 0))
	})
}
