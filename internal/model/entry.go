package model

import "time"

// Entry is one decrypted object as the filesystem and panel see it.
type Entry struct {
	ID         ObjectID
	ParentID   ObjectID
	Name       string
	Kind       Kind
	Size       int64
	ModTime    time.Time
	State      LocalState
	Executable bool
	LocalOnly  bool
}

func (e Entry) IsFolder() bool {
	return e.Kind == KindFolder
}
