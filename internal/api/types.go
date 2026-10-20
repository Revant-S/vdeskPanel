// Package api is the local HTTP-over-Unix-socket interface between the daemon and its
// clients, the panel and the CLI. This file is the contract both sides build on.
package api

import "time"

type VaultState string

const (
	StateNeedsSignIn VaultState = "needs-signin"
	StateNeedsSetup  VaultState = "needs-setup"
	StateLocked      VaultState = "locked"
	StateUnlocking   VaultState = "unlocking"
	StateRestoring   VaultState = "restoring"
	StateMounted     VaultState = "mounted"
	StateLocking     VaultState = "locking"
)

type Status struct {
	State            VaultState       `json:"state"`
	VaultExists      bool             `json:"vaultExists"`
	DeviceName       string           `json:"deviceName"`
	MountPoint       string           `json:"mountPoint"`
	Online           bool             `json:"online"`
	SyncingItems     int              `json:"syncingItems"`
	NotBackedUpFiles int              `json:"notBackedUpFiles"`
	NotBackedUpBytes int64            `json:"notBackedUpBytes"`
	LastBackedUp     time.Time        `json:"lastBackedUp,omitzero"`
	BytesPerSecond   int64            `json:"bytesPerSecond"`
	CorruptFiles     int              `json:"corruptFiles"`
	KeysMaySwap      bool             `json:"keysMaySwap"`
	Restore          *RestoreProgress `json:"restore,omitempty"`
	Message          string           `json:"message,omitempty"`
}

type RestoreProgress struct {
	FilesDone  int   `json:"filesDone"`
	FilesTotal int   `json:"filesTotal"`
	BytesDone  int64 `json:"bytesDone"`
	BytesTotal int64 `json:"bytesTotal"`
	Paused     bool  `json:"paused"`
	Complete   bool  `json:"complete"`
}

type FileState string

const (
	FileCloudOnly FileState = "cloud"
	FilePartial   FileState = "partial"
	FileLocal     FileState = "local"
	FileDirty     FileState = "dirty"
	FileCorrupt   FileState = "corrupt"
)

type TreeEntry struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Folder    bool      `json:"folder"`
	Size      int64     `json:"size"`
	LocalSize int64     `json:"localSize"`
	State     FileState `json:"state"`
	ModTime   time.Time `json:"modTime"`
}

type OpView struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type CommitView struct {
	ID         string    `json:"id"`
	Device     string    `json:"device"`
	DeviceName string    `json:"deviceName"`
	Clock      string    `json:"clock"`
	Time       time.Time `json:"time"`
	Parents    []string  `json:"parents"`
	Summary    string    `json:"summary"`
	Ops        []OpView  `json:"ops"`
}

type DeviceView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	FirstSeen time.Time `json:"firstSeen,omitzero"`
	Retired   bool      `json:"retired"`
	This      bool      `json:"this"`
}

type CommitPage struct {
	Commits []CommitView `json:"commits"`
	Devices []DeviceView `json:"devices"`
	// NextBefore pages to older commits; empty when there are none.
	NextBefore string `json:"nextBefore,omitempty"`
}

type TrashItem struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	OriginalPath string    `json:"originalPath"`
	Reason       string    `json:"reason"`
	Device       string    `json:"device"`
	DeviceName   string    `json:"deviceName"`
	DeletedAt    time.Time `json:"deletedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Size         int64     `json:"size"`
	FileCount    int       `json:"fileCount"`
	Folder       bool      `json:"folder"`
}

type EmptyTrashResult struct {
	Items int   `json:"items"`
	Bytes int64 `json:"bytes"`
}

type VerifyProblem struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Trashed bool   `json:"trashed"`
}

type VerifyReport struct {
	Deep         bool            `json:"deep"`
	FilesChecked int             `json:"filesChecked"`
	Problems     []VerifyProblem `json:"problems"`
}

type Settings struct {
	MountPoint          string   `json:"mountPoint"`
	DeviceName          string   `json:"deviceName"`
	Uploads             int      `json:"uploads"`
	Downloads           int      `json:"downloads"`
	ActivePollSeconds   int      `json:"activePollSeconds"`
	IdlePollSeconds     int      `json:"idlePollSeconds"`
	TrashRetentionDays  int      `json:"trashRetentionDays"`
	ThumbnailerDenyList []string `json:"thumbnailerDenyList"`
}

type JoinMode string

const (
	JoinRestoreEverything JoinMode = "restore"
	JoinOnDemand          JoinMode = "on-demand"
)

type EventKind string

const (
	EventStatus EventKind = "status"
	EventCommit EventKind = "commit"
	EventTrash  EventKind = "trash"
	EventTree   EventKind = "tree"
)

// Event is pushed on GET /events. Status events carry the new status; the others tell a
// client which view to reload.
type Event struct {
	Kind   EventKind `json:"kind"`
	Status *Status   `json:"status,omitempty"`
}

type ErrorBody struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

const (
	CodeWrongPassword  = "wrong-password"
	CodeLocked         = "locked"
	CodeNotFound       = "not-found"
	CodeBusy           = "busy"
	CodeNewerFormat    = "newer-format"
	CodeInvalidRequest = "invalid-request"
)
