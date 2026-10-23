// Package devices keeps the encrypted device records under Vault/devices/ on Drive.
package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"vault/internal/crypto"
	"vault/internal/model"
)

const recordSuffix = ".vdev"

type Record struct {
	ID        model.DeviceID `json:"id"`
	Name      string         `json:"name"`
	FirstSeen time.Time      `json:"firstSeen"`
	Retired   bool           `json:"retired"`
	Updated   time.Time      `json:"updated"`
}

type Remote interface {
	ListFolder(ctx context.Context, folderID string) ([]model.RemoteFile, error)
	Download(ctx context.Context, driveID string) ([]byte, error)
	Upload(ctx context.Context, folderID, name string, properties map[string]string, data []byte) error
	Delete(ctx context.Context, driveID string) error
}

type Deps struct {
	Remote  Remote
	Keyring *crypto.Keyring
	Random  io.Reader
	Now     func() time.Time
	Logger  *slog.Logger
}

type Config struct {
	VaultID  string
	FolderID string
}

// Registry reads and writes device records. It remembers the last listing so callers that
// run often, like the history view, never wait on Drive.
type Registry struct {
	deps   Deps
	config Config

	mu     sync.Mutex
	cached map[model.DeviceID]Record
}

func New(deps Deps, config Config) *Registry {
	return &Registry{deps: deps, config: config, cached: map[model.DeviceID]Record{}}
}

func (r *Registry) Cached() map[model.DeviceID]Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[model.DeviceID]Record, len(r.cached))
	for id, record := range r.cached {
		out[id] = record
	}
	return out
}

// Refresh lists every record on Drive. A record that fails to open is skipped and logged:
// it can only come from another vault or tampering.
func (r *Registry) Refresh(ctx context.Context) (map[model.DeviceID]Record, error) {
	_, records, err := r.load(ctx)
	return records, err
}

func (r *Registry) load(ctx context.Context) (map[model.DeviceID][]model.RemoteFile, map[model.DeviceID]Record, error) {
	files, err := r.recordFiles(ctx)
	if err != nil {
		return nil, nil, err
	}
	records := map[model.DeviceID]Record{}
	for id, versions := range files {
		for _, file := range versions {
			record, err := r.download(ctx, id, file.ID)
			if err != nil {
				r.deps.Logger.Warn("unreadable device record", "device", id, "err", err)
				continue
			}
			if current, ok := records[id]; !ok || record.Updated.After(current.Updated) {
				records[id] = record
			}
		}
	}
	r.mu.Lock()
	r.cached = records
	r.mu.Unlock()
	return files, records, nil
}

// Publish applies change to the device's newest record (or a new one), uploads the result
// and then deletes the older copies. A crash in between leaves two copies; readers take the
// newest.
func (r *Registry) Publish(ctx context.Context, id model.DeviceID, change func(*Record)) error {
	files, records, err := r.load(ctx)
	if err != nil {
		return err
	}
	record, ok := records[id]
	if !ok {
		record = Record{ID: id, FirstSeen: r.deps.Now()}
	}
	before := record
	change(&record)
	if ok && record == before {
		return nil
	}
	record.Updated = r.deps.Now()
	if err := r.upload(ctx, record); err != nil {
		return err
	}
	for _, old := range files[id] {
		if err := r.deps.Remote.Delete(ctx, old.ID); err != nil {
			r.deps.Logger.Warn("delete old device record", "device", id, "err", err)
		}
	}
	r.mu.Lock()
	r.cached[id] = record
	r.mu.Unlock()
	return nil
}

func (r *Registry) recordFiles(ctx context.Context) (map[model.DeviceID][]model.RemoteFile, error) {
	listed, err := r.deps.Remote.ListFolder(ctx, r.config.FolderID)
	if err != nil {
		return nil, fmt.Errorf("list device records: %w", err)
	}
	files := map[model.DeviceID][]model.RemoteFile{}
	for _, file := range listed {
		id, ok := strings.CutSuffix(file.Name, recordSuffix)
		if ok && !file.Folder && !file.Trashed {
			files[model.DeviceID(id)] = append(files[model.DeviceID(id)], file)
		}
	}
	return files, nil
}

func (r *Registry) upload(ctx context.Context, record Record) error {
	plaintext, err := json.Marshal(record)
	if err != nil {
		return err
	}
	var sealed []byte
	err = r.deps.Keyring.Use(func(k *crypto.Keys) error {
		sealed, err = crypto.Seal(k.Log, r.deps.Random, plaintext, crypto.DeviceAD(string(record.ID)))
		return err
	})
	if err != nil {
		return fmt.Errorf("seal device record: %w", err)
	}
	return r.deps.Remote.Upload(ctx, r.config.FolderID, string(record.ID)+recordSuffix,
		map[string]string{"vid": r.config.VaultID}, sealed)
}

func (r *Registry) download(ctx context.Context, id model.DeviceID, driveID string) (Record, error) {
	sealed, err := r.deps.Remote.Download(ctx, driveID)
	if err != nil {
		return Record{}, err
	}
	var record Record
	err = r.deps.Keyring.Use(func(k *crypto.Keys) error {
		plaintext, err := crypto.Open(k.Log, sealed, crypto.DeviceAD(string(id)))
		if err != nil {
			return err
		}
		return json.Unmarshal(plaintext, &record)
	})
	if err == nil && record.ID != id {
		err = fmt.Errorf("record claims device %s", record.ID)
	}
	return record, err
}
