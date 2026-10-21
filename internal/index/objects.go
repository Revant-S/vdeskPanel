package index

import (
	"fmt"
	"iter"
	"time"

	"github.com/cosmoboy/vault/internal/model"
)

type Object struct {
	ID              model.ObjectID
	ParentID        model.ObjectID
	Kind            model.Kind
	EncName         string
	DriveID         string
	Size            int64
	ModTime         time.Time
	Executable      bool
	ContentMAC      []byte
	BodyID          []byte
	BodyMD5         string
	ContentVer      model.Stamp
	PlacementVer    model.Stamp
	DeleteVer       model.Stamp
	State           model.LocalState
	TrashID         string
	LocalOnly       bool
	DeferredContent []byte
}

const objectColumns = `id, parent_id, kind, enc_name, drive_id, size, mtime, executable, content_mac, body_id,
	body_md5, content_ver, placement_ver, delete_ver, local_state, trash_id, local_only, deferred_content`

func (t *Tx) PutObject(o Object) error {
	deferred, err := t.seal("objects", "deferred_content", string(o.ID), o.DeferredContent)
	if err != nil {
		return err
	}
	_, err = t.exec(`INSERT OR REPLACE INTO objects (`+objectColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, o.ParentID, o.Kind, o.EncName, o.DriveID, o.Size, unixNanos(o.ModTime), boolInt(o.Executable),
		o.ContentMAC, o.BodyID, o.BodyMD5, o.ContentVer.String(), o.PlacementVer.String(), o.DeleteVer.String(),
		o.State, o.TrashID, boolInt(o.LocalOnly), deferred)
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

func (t *Tx) Object(id model.ObjectID) (Object, error) {
	row := t.queryRow(`SELECT `+objectColumns+` FROM objects WHERE id = ?`, id)
	object, err := t.scanObject(row)
	if err != nil {
		return Object{}, fmt.Errorf("object %s: %w", id, notFoundOr(err))
	}
	return object, nil
}

func (t *Tx) DeleteObject(id model.ObjectID) error {
	if _, err := t.exec(`DELETE FROM objects WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

func (t *Tx) SetLocalState(id model.ObjectID, state model.LocalState) error {
	if _, err := t.exec(`UPDATE objects SET local_state = ? WHERE id = ?`, state, id); err != nil {
		return fmt.Errorf("set local state: %w", err)
	}
	return nil
}

func (t *Tx) ChildrenOf(parent model.ObjectID) ([]Object, error) {
	return t.selectObjects(`WHERE parent_id = ? ORDER BY id`, parent)
}

func (t *Tx) ObjectsByContentMAC(mac []byte) ([]Object, error) {
	return t.selectObjects(`WHERE content_mac = ? ORDER BY id`, mac)
}

func (t *Tx) ObjectsByDriveID(driveID string) ([]Object, error) {
	return t.selectObjects(`WHERE drive_id = ? ORDER BY id`, driveID)
}

func (t *Tx) ObjectsWithTrashID(trashID string) ([]Object, error) {
	return t.selectObjects(`WHERE trash_id = ? ORDER BY id`, trashID)
}

func (t *Tx) ObjectsInState(state model.LocalState) ([]Object, error) {
	return t.selectObjects(`WHERE local_state = ? ORDER BY id`, state)
}

func (t *Tx) AllObjects() iter.Seq2[Object, error] {
	return func(yield func(Object, error) bool) {
		rows, err := t.query(`SELECT ` + objectColumns + ` FROM objects ORDER BY id`)
		if err != nil {
			yield(Object{}, fmt.Errorf("list objects: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			object, err := t.scanObject(rows)
			if !yield(object, err) || err != nil {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Object{}, fmt.Errorf("list objects: %w", err))
		}
	}
}

func (t *Tx) selectObjects(where string, args ...any) ([]Object, error) {
	rows, err := t.query(`SELECT `+objectColumns+` FROM objects `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("select objects: %w", err)
	}
	defer rows.Close()
	var objects []Object
	for rows.Next() {
		object, err := t.scanObject(rows)
		if err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return objects, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func (t *Tx) scanObject(row scanner) (Object, error) {
	var o Object
	var mtime int64
	var executable, localOnly int
	var contentVer, placementVer, deleteVer string
	var deferred []byte
	err := row.Scan(&o.ID, &o.ParentID, &o.Kind, &o.EncName, &o.DriveID, &o.Size, &mtime, &executable,
		&o.ContentMAC, &o.BodyID, &o.BodyMD5, &contentVer, &placementVer, &deleteVer, &o.State, &o.TrashID,
		&localOnly, &deferred)
	if err != nil {
		return Object{}, err
	}
	o.ModTime, o.Executable, o.LocalOnly = fromUnixNanos(mtime), executable == 1, localOnly == 1
	if o.ContentVer, err = model.ParseStamp(contentVer); err != nil {
		return Object{}, err
	}
	if o.PlacementVer, err = model.ParseStamp(placementVer); err != nil {
		return Object{}, err
	}
	if o.DeleteVer, err = model.ParseStamp(deleteVer); err != nil {
		return Object{}, err
	}
	o.DeferredContent, err = t.open("objects", "deferred_content", string(o.ID), deferred)
	return o, err
}
