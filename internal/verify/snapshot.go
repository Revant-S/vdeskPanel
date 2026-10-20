package verify

import (
	"context"
	"fmt"
	"path"

	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
)

// snapshot is the part of the index a verify compares against Drive.
type snapshot struct {
	objects  map[model.ObjectID]index.Object
	children map[model.ObjectID][]index.Object
	trash    []index.TrashRecord
	paths    map[model.ObjectID]string
	names    NameDecrypter
}

func (v *Verifier) loadSnapshot(ctx context.Context) (*snapshot, error) {
	s := &snapshot{objects: map[model.ObjectID]index.Object{}, children: map[model.ObjectID][]index.Object{},
		paths: map[model.ObjectID]string{model.RootID: "/"}, names: v.deps.Names}
	err := v.deps.Index.View(ctx, func(tx *index.Tx) error {
		for object, err := range tx.AllObjects() {
			if err != nil {
				return err
			}
			s.objects[object.ID] = object
			if isLive(object) && object.DriveID != "" && !object.LocalOnly {
				s.children[object.ParentID] = append(s.children[object.ParentID], object)
			}
		}
		var err error
		s.trash, err = tx.TrashRecords()
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("verify: load index: %w", err)
	}
	return s, nil
}

// isLive applies the delete tombstone rule: content newer than the delete keeps an object.
func isLive(object index.Object) bool {
	return object.TrashID == "" && (object.DeleteVer.IsZero() || object.ContentVer.After(object.DeleteVer))
}

// pathOf decrypts names up to the root. The path is for the report only, never for logs.
func (s *snapshot) pathOf(id model.ObjectID) string {
	if known, ok := s.paths[id]; ok {
		return known
	}
	object, ok := s.objects[id]
	if !ok {
		return "/?" + string(id)
	}
	name, err := s.names.DecryptName(id, object.EncName)
	if err != nil {
		name = "?" + string(id)
	}
	full := path.Join(s.pathOf(object.ParentID), name)
	s.paths[id] = full
	return full
}

func (s *snapshot) fileCheck(object index.Object) bodyCheck {
	return bodyCheck{id: object.ID, path: s.pathOf(object.ID), driveID: object.DriveID, bodyMD5: object.BodyMD5,
		bodyID: object.BodyID, contentMAC: object.ContentMAC}
}

// trashCheck describes a trash item. A trashed file's object row still holds its body
// facts when it points at the same Drive file (D32); folders only need to exist.
func (s *snapshot) trashCheck(record index.TrashRecord) (bodyCheck, bool) {
	check := bodyCheck{id: record.ObjectID, path: record.OriginalPath, trashed: true, driveID: record.DriveID}
	object, ok := s.objects[record.ObjectID]
	if record.Folder || !ok || object.DriveID != record.DriveID {
		return check, false
	}
	check.bodyMD5, check.bodyID, check.contentMAC = object.BodyMD5, object.BodyID, object.ContentMAC
	return check, true
}
