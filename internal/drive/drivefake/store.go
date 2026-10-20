package drivefake

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"maps"
	"net/http"
	"slices"
	"time"

	drive "google.golang.org/api/drive/v3"

	"github.com/cosmoboy/vault/internal/model"
)

const (
	folderMimeType = "application/vnd.google-apps.folder"
	rootID         = "root"
)

type storedFile struct {
	id         string
	name       string
	mimeType   string
	parents    []string
	properties map[string]string
	content    []byte
	md5        string
	created    time.Time
	modified   time.Time
	trashed    bool
}

func (f *storedFile) isFolder() bool { return f.mimeType == folderMimeType }

func (f *storedFile) hasParent(id string) bool { return slices.Contains(f.parents, id) }

func (f *storedFile) setContent(content []byte) {
	f.content = content
	sum := md5.Sum(content)
	f.md5 = hex.EncodeToString(sum[:])
}

func (f *storedFile) toDrive() *drive.File {
	file := &drive.File{
		Kind:          "drive#file",
		Id:            f.id,
		Name:          f.name,
		MimeType:      f.mimeType,
		Parents:       slices.Clone(f.parents),
		AppProperties: maps.Clone(f.properties),
		CreatedTime:   formatTime(f.created),
		ModifiedTime:  formatTime(f.modified),
		Trashed:       f.trashed,
	}
	if !f.isFolder() {
		file.Md5Checksum = f.md5
		file.Size = int64(len(f.content))
		file.ForceSendFields = []string{"Size"}
	}
	return file
}

func (f *storedFile) toRemote() model.RemoteFile {
	return model.RemoteFile{
		ID:           f.id,
		Name:         f.name,
		MD5:          f.md5,
		Parents:      slices.Clone(f.parents),
		Size:         int64(len(f.content)),
		ModifiedTime: f.modified,
		CreatedTime:  f.created,
		Folder:       f.isFolder(),
		Trashed:      f.trashed,
		Properties:   maps.Clone(f.properties),
	}
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

type changeRecord struct {
	fileID  string
	removed bool
	file    *drive.File
}

// store is the fake's whole state; the Server's mutex guards it.
type store struct {
	files      map[string]*storedFile
	unusedIDs  map[string]bool
	changes    []changeRecord
	firstToken int64
	sessions   map[string]*uploadSession
	now        func() time.Time
}

func newStore(now func() time.Time) *store {
	return &store{
		files:     map[string]*storedFile{},
		unusedIDs: map[string]bool{},
		sessions:  map[string]*uploadSession{},
		now:       now,
	}
}

func (s *store) generateID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func (s *store) issueIDs(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = s.generateID()
		s.unusedIDs[ids[i]] = true
	}
	return ids
}

// claimID checks a client-supplied ID the way Drive does: it must come from generateIds
// and must not already name a file.
func (s *store) claimID(requested string) (string, *apiError) {
	if requested == "" {
		return s.generateID(), nil
	}
	if _, exists := s.files[requested]; exists {
		return "", failure(http.StatusConflict, "duplicate", "A file already exists with the provided ID.")
	}
	if !s.unusedIDs[requested] {
		return "", badRequest("The provided file ID is not usable.")
	}
	delete(s.unusedIDs, requested)
	return requested, nil
}

func (s *store) checkParents(parents []string) *apiError {
	for _, parent := range parents {
		if parent == rootID {
			continue
		}
		if file, ok := s.files[parent]; !ok || !file.isFolder() {
			return notFound(parent)
		}
	}
	return nil
}

func (s *store) insert(metadata *drive.File, content []byte) (*storedFile, *apiError) {
	if err := s.checkParents(metadata.Parents); err != nil {
		return nil, err
	}
	id, err := s.claimID(metadata.Id)
	if err != nil {
		return nil, err
	}
	now := s.now()
	file := &storedFile{
		id:         id,
		name:       metadata.Name,
		mimeType:   metadata.MimeType,
		parents:    slices.Clone(metadata.Parents),
		properties: maps.Clone(metadata.AppProperties),
		created:    now,
		modified:   parseTimeOr(metadata.ModifiedTime, now),
	}
	if file.mimeType == "" {
		file.mimeType = "application/octet-stream"
	}
	if len(file.parents) == 0 {
		file.parents = []string{rootID}
	}
	if !file.isFolder() {
		file.setContent(content)
	}
	s.files[id] = file
	s.recordChange(file)
	return file, nil
}

func (s *store) recordChange(file *storedFile) {
	s.changes = append(s.changes, changeRecord{fileID: file.id, file: file.toDrive()})
}

func (s *store) remove(id string) {
	for _, child := range s.files {
		if child.hasParent(id) {
			s.remove(child.id)
		}
	}
	delete(s.files, id)
	s.changes = append(s.changes, changeRecord{fileID: id, removed: true})
}

func (s *store) nextToken() int64 { return int64(len(s.changes)) }

func parseTimeOr(value string, fallback time.Time) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return fallback
	}
	return parsed
}
