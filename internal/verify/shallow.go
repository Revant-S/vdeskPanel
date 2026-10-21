package verify

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
)

type folderVisit struct {
	id      model.ObjectID
	driveID string
}

type listing map[string]model.RemoteFile

// checkListings walks the tree level by level with one listing per folder, then trash/. It
// returns the bodies found on Drive with the checksum the index expects.
func (v *Verifier) checkListings(ctx context.Context, s *snapshot, report *Report) ([]*bodyCheck, error) {
	var present []*bodyCheck
	level := []folderVisit{{id: model.RootID, driveID: v.config.TreeFolderID}}
	for len(level) > 0 {
		listings, err := v.listFolders(ctx, level)
		if err != nil {
			return nil, err
		}
		report.FoldersListed += len(level)
		var next []folderVisit
		for i, folder := range level {
			found, subfolders := compareFolder(s, s.children[folder.id], listings[i], report)
			present, next = append(present, found...), append(next, subfolders...)
		}
		level = next
	}
	inTrash, err := v.checkTrash(ctx, s, report)
	if err != nil {
		return nil, err
	}
	return append(present, inTrash...), nil
}

func (v *Verifier) listFolders(ctx context.Context, folders []folderVisit) ([]listing, error) {
	listings := make([]listing, len(folders))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(v.config.Listings)
	for i, folder := range folders {
		group.Go(func() error {
			var err error
			listings[i], err = v.list(groupCtx, folder.driveID)
			if err != nil {
				return wrapRemote("list folder "+string(folder.id), err)
			}
			return nil
		})
	}
	return listings, group.Wait()
}

func (v *Verifier) list(ctx context.Context, driveID string) (listing, error) {
	files, err := v.deps.Remote.ListChildren(ctx, driveID)
	if err != nil {
		return nil, err
	}
	byID := make(listing, len(files))
	for _, file := range files {
		byID[file.ID] = file
	}
	return byID, nil
}

// compareFolder reports a missing folder once, without descending into it.
func compareFolder(s *snapshot, expected []index.Object, remote listing, report *Report) ([]*bodyCheck, []folderVisit) {
	var present []*bodyCheck
	var subfolders []folderVisit
	for _, object := range expected {
		file, found := remote[object.DriveID]
		if object.Kind == model.KindFolder {
			if found {
				subfolders = append(subfolders, folderVisit{id: object.ID, driveID: object.DriveID})
			} else {
				report.Problems = append(report.Problems, s.fileCheck(object).problem(MissingOnDrive))
			}
			continue
		}
		report.FilesChecked++
		check := s.fileCheck(object)
		switch {
		case !found:
			report.Problems = append(report.Problems, check.problem(MissingOnDrive))
		case file.MD5 != object.BodyMD5:
			report.Problems = append(report.Problems, check.problem(ChecksumMismatch))
		default:
			present = append(present, check)
		}
	}
	return present, subfolders
}

func (v *Verifier) checkTrash(ctx context.Context, s *snapshot, report *Report) ([]*bodyCheck, error) {
	if len(s.trash) == 0 {
		return nil, nil
	}
	remote, err := v.list(ctx, v.config.TrashFolderID)
	if err != nil {
		return nil, wrapRemote("list trash", err)
	}
	var present []*bodyCheck
	for _, record := range s.trash {
		if record.DriveID == "" {
			continue
		}
		report.TrashChecked++
		check, hasBody := s.trashCheck(record)
		file, found := remote[record.DriveID]
		switch {
		case !found:
			report.Problems = append(report.Problems, check.problem(MissingOnDrive))
		case hasBody && file.MD5 != check.bodyMD5:
			report.Problems = append(report.Problems, check.problem(ChecksumMismatch))
		case hasBody:
			present = append(present, check)
		}
	}
	return present, nil
}
