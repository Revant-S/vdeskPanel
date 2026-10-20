package index

import (
	"fmt"
	"time"

	"github.com/cosmoboy/vault/internal/model"
)

type TrashRecord struct {
	ID           string
	ObjectID     model.ObjectID
	OriginalPath string
	DeletedAt    time.Time
	Device       model.DeviceID
	Reason       string
	Size         int64
	FileCount    int
	Folder       bool
	ContentMAC   []byte
	DriveID      string
}

const trashColumns = `id, object_id, enc_path, deleted_at, device, reason, size, file_count, folder, content_mac, drive_id`

func (t *Tx) PutTrash(r TrashRecord) error {
	path, err := t.seal("trash", "enc_path", r.ID, []byte(r.OriginalPath))
	if err != nil {
		return err
	}
	_, err = t.exec(`INSERT OR REPLACE INTO trash (`+trashColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ObjectID, path, unixNanos(r.DeletedAt), r.Device, r.Reason, r.Size, r.FileCount,
		boolInt(r.Folder), r.ContentMAC, r.DriveID)
	if err != nil {
		return fmt.Errorf("put trash: %w", err)
	}
	return nil
}

func (t *Tx) Trash(id string) (TrashRecord, error) {
	records, err := t.selectTrash(`WHERE id = ?`, id)
	if err != nil {
		return TrashRecord{}, err
	}
	if len(records) == 0 {
		return TrashRecord{}, fmt.Errorf("trash %s: %w", id, ErrNotFound)
	}
	return records[0], nil
}

func (t *Tx) TrashRecords() ([]TrashRecord, error) {
	return t.selectTrash(`ORDER BY deleted_at DESC, id`)
}

func (t *Tx) TrashByContentMAC(mac []byte) ([]TrashRecord, error) {
	return t.selectTrash(`WHERE content_mac = ? ORDER BY id`, mac)
}

func (t *Tx) DeleteTrash(id string) error {
	if _, err := t.exec(`DELETE FROM trash WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete trash: %w", err)
	}
	return nil
}

func (t *Tx) selectTrash(where string, args ...any) ([]TrashRecord, error) {
	rows, err := t.query(`SELECT `+trashColumns+` FROM trash `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("select trash: %w", err)
	}
	defer rows.Close()
	var records []TrashRecord
	for rows.Next() {
		var r TrashRecord
		var path []byte
		var deleted int64
		var folder int
		if err := rows.Scan(&r.ID, &r.ObjectID, &path, &deleted, &r.Device, &r.Reason, &r.Size, &r.FileCount, &folder, &r.ContentMAC, &r.DriveID); err != nil {
			return nil, err
		}
		plainPath, err := t.open("trash", "enc_path", r.ID, path)
		if err != nil {
			return nil, err
		}
		r.OriginalPath, r.DeletedAt, r.Folder = string(plainPath), fromUnixNanos(deleted), folder == 1
		records = append(records, r)
	}
	return records, rows.Err()
}
