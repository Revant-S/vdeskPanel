package journal

import (
	"time"

	"github.com/cosmoboy/vault/internal/model"
)

type OpKind string

const (
	OpCreate       OpKind = "create"
	OpModify       OpKind = "modify"
	OpRename       OpKind = "rename"
	OpMove         OpKind = "move"
	OpDelete       OpKind = "delete"
	OpMovedOut     OpKind = "moved_out"
	OpSuperseded   OpKind = "superseded"
	OpTrashRestore OpKind = "trash_restore"
	OpTrashPurge   OpKind = "trash_purge"
)

// Op carries everything a remote device needs to apply it without extra Drive calls.
type Op struct {
	Kind       OpKind         `json:"kind"`
	ObjectID   model.ObjectID `json:"oid"`
	ParentID   model.ObjectID `json:"parent,omitempty"`
	Path       string         `json:"path,omitempty"`
	From       string         `json:"from,omitempty"`
	To         string         `json:"to,omitempty"`
	EncName    string         `json:"encName,omitempty"`
	Folder     bool           `json:"folder,omitempty"`
	Size       int64          `json:"size,omitempty"`
	ModTime    time.Time      `json:"mtime,omitzero"`
	Executable bool           `json:"exec,omitempty"`
	ContentMAC []byte         `json:"mac,omitempty"`
	BodyID     []byte         `json:"bodyId,omitempty"`
	BodyMD5    string         `json:"md5,omitempty"`
	DriveID    string         `json:"driveId,omitempty"`
	// PreviousDriveID is the version this content replaced, so a receiver can tell an edit
	// made on top of its own version from a concurrent one.
	PreviousDriveID string         `json:"prevDriveId,omitempty"`
	Version         model.Stamp    `json:"ver,omitzero"`
	ReplacedFrom    model.ObjectID `json:"replacedFrom,omitempty"`
	Trash           *TrashRef      `json:"trash,omitempty"`
	Reason          string         `json:"reason,omitempty"`
	FileCount       int            `json:"files,omitempty"`
}

type TrashRef struct {
	ID      string `json:"id"`
	DriveID string `json:"driveId,omitempty"`
	// PreviousDriveID is the version this content replaced, so a receiver can tell an edit
	// made on top of its own version from a concurrent one.
	PreviousDriveID string         `json:"prevDriveId,omitempty"`
	Reason          string         `json:"reason"`
	OriginalPath    string         `json:"path"`
	Size            int64          `json:"size,omitempty"`
	FileCount       int            `json:"files,omitempty"`
	Folder          bool           `json:"folder,omitempty"`
	ObjectID        model.ObjectID `json:"oid,omitempty"`
	ContentMAC      []byte         `json:"mac,omitempty"`
}
