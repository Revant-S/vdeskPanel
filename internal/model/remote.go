package model

import "time"

type RemoteFile struct {
	ID           string
	Name         string
	MD5          string
	Parents      []string
	Size         int64
	ModifiedTime time.Time
	CreatedTime  time.Time
	Folder       bool
	Trashed      bool
	Properties   map[string]string
}

type RemoteChange struct {
	FileID  string
	Removed bool
	File    *RemoteFile
}

type ChangesPage struct {
	Changes           []RemoteChange
	NextPageToken     string
	NewStartPageToken string
}
