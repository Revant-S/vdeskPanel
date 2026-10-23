package workspace

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"vault/internal/index"
	"vault/internal/model"
)

var ErrRestoreWhileLive = errors.New("workspace: the original is in the vault again; move it aside to restore this version")

// RestoreTrash puts a trash item back at its original path, recreating missing folders and
// renaming it "name (restored).ext" if the path is taken. It returns the restored path.
func (w *Workspace) RestoreTrash(ctx context.Context, trashID string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	record, rows, err := w.trashItem(ctx, trashID)
	if err != nil {
		return "", err
	}
	if _, live := w.tree.Get(record.ObjectID); live {
		return "", ErrRestoreWhileLive
	}
	parentID, err := w.ensureFolders(ctx, path.Dir(record.OriginalPath))
	if err != nil {
		return "", err
	}
	name := w.restoredName(parentID, path.Base(record.OriginalPath))
	restoredPath := path.Join(path.Dir(record.OriginalPath), name)
	if err := w.persistRestore(ctx, record, rows, parentID, name, restoredPath); err != nil {
		return "", err
	}
	return restoredPath, w.insertRestored(ctx, record.ObjectID)
}

func (w *Workspace) trashItem(ctx context.Context, trashID string) (index.TrashRecord, []index.Object, error) {
	var record index.TrashRecord
	var rows []index.Object
	err := w.Index.View(ctx, func(tx *index.Tx) error {
		var err error
		if record, err = tx.Trash(trashID); err != nil {
			return err
		}
		rows, err = tx.ObjectsWithTrashID(trashID)
		return err
	})
	if errors.Is(err, index.ErrNotFound) {
		return record, nil, fmt.Errorf("trash item %s: %w", trashID, errNotIndexed)
	}
	return record, rows, err
}

// ensureFolders returns the folder at dir, creating any part of it that is missing.
func (w *Workspace) ensureFolders(ctx context.Context, dir string) (model.ObjectID, error) {
	parent := model.RootID
	for _, part := range strings.Split(strings.Trim(dir, "/"), "/") {
		if part == "" {
			continue
		}
		if existing, ok := w.tree.Lookup(parent, part); ok && existing.IsFolder() {
			parent = existing.ID
			continue
		}
		created, err := w.createObject(ctx, parent, w.restoredName(parent, part), model.KindFolder, false)
		if err != nil {
			return "", err
		}
		parent = created.ID
	}
	return parent, nil
}

func (w *Workspace) restoredName(parent model.ObjectID, name string) string {
	if _, taken := w.tree.Lookup(parent, name); !taken {
		return name
	}
	extension := path.Ext(name)
	base := strings.TrimSuffix(name, extension)
	candidate := base + " (restored)" + extension
	for n := 2; ; n++ {
		if _, taken := w.tree.Lookup(parent, candidate); !taken {
			return candidate
		}
		candidate = fmt.Sprintf("%s (restored %d)%s", base, n, extension)
	}
}

func (w *Workspace) persistRestore(ctx context.Context, record index.TrashRecord, rows []index.Object, parentID model.ObjectID, name, restoredPath string) error {
	encName, err := w.Names.EncryptName(record.ObjectID, name)
	if err != nil {
		return err
	}
	stamp := w.Clock.Next()
	return w.update(ctx, func(tx *index.Tx) error {
		for _, row := range rows {
			row.TrashID, row.DeleteVer = "", model.Stamp{}
			if row.ID == record.ObjectID {
				row.ParentID, row.EncName, row.PlacementVer = parentID, encName, stamp
			}
			if row.Kind == model.KindFile {
				row.State = model.CloudOnly
			}
			if err := tx.PutObject(row); err != nil {
				return err
			}
		}
		if err := tx.DeleteTrash(record.ID); err != nil {
			return err
		}
		op := model.LocalOp{Kind: model.LocalRestore, ObjectID: record.ObjectID, Folder: record.Folder, ParentID: parentID,
			Name: name, Path: restoredPath, TrashID: record.ID, DriveID: record.DriveID}
		return appendOp(tx, op, stamp, w.Now())
	})
}

func (w *Workspace) insertRestored(ctx context.Context, top model.ObjectID) error {
	return w.Index.View(ctx, func(tx *index.Tx) error { return w.insertSubtree(tx, top) })
}

// insertSubtree adds an object and everything live under it back into the tree.
func (w *Workspace) insertSubtree(tx *index.Tx, top model.ObjectID) error {
	queue := []model.ObjectID{top}
	for len(queue) > 0 {
		object, err := tx.Object(queue[0])
		queue = queue[1:]
		if err != nil {
			return err
		}
		name, err := w.Names.DecryptName(object.ID, object.EncName)
		if err != nil {
			return err
		}
		if err := w.tree.Insert(entryOfObject(object, name)); err != nil {
			return err
		}
		children, err := tx.ChildrenOf(object.ID)
		if err != nil {
			return err
		}
		for _, child := range children {
			if isLive(child) {
				queue = append(queue, child.ID)
			}
		}
	}
	return nil
}

// PurgeTrash deletes a trash item for good. It leaves the trash view at once; the Drive
// delete, the log entry and dropping its rows follow with the next batch. The rows wait for
// Drive because another device may have restored the item first, and its restore needs them.
func (w *Workspace) PurgeTrash(ctx context.Context, trashID string) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	record, _, err := w.trashItem(ctx, trashID)
	if err != nil {
		return 0, err
	}
	err = w.update(ctx, func(tx *index.Tx) error {
		if err := tx.DeleteTrash(record.ID); err != nil {
			return err
		}
		subject := record.ObjectID
		if subject == "" {
			subject = model.ObjectID(record.ID)
		}
		op := model.LocalOp{Kind: model.LocalPurge, ObjectID: subject, Folder: record.Folder, Path: record.OriginalPath,
			TrashID: record.ID, DriveID: record.DriveID}
		return appendOp(tx, op, w.Clock.Next(), w.Now())
	})
	return record.Size, err
}

// ExpireTrash purges every trash item deleted before the retention period.
func (w *Workspace) ExpireTrash(ctx context.Context, retention time.Duration) (int, error) {
	var expired []string
	cutoff := w.Now().Add(-retention)
	err := w.Index.View(ctx, func(tx *index.Tx) error {
		records, err := tx.TrashRecords()
		for _, record := range records {
			if record.DeletedAt.Before(cutoff) {
				expired = append(expired, record.ID)
			}
		}
		return err
	})
	for _, id := range expired {
		if _, purgeErr := w.PurgeTrash(ctx, id); purgeErr != nil && !errors.Is(purgeErr, errNotIndexed) {
			err = errors.Join(err, purgeErr)
		}
	}
	return len(expired), err
}
