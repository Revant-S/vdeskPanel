package restore

import (
	"slices"
	"strings"
	"time"

	"vault/internal/index"
	"vault/internal/model"
)

// Progress counts the files of the current or last round. In OnDemand mode it covers only
// the wanted files.
type Progress struct {
	FilesDone   int
	FilesTotal  int
	BytesDone   int64
	BytesTotal  int64
	FilesFailed int
	Paused      bool
	// Complete is set once a round ended with every counted file Local.
	Complete bool
}

type FileFailure struct {
	ID  model.ObjectID
	Err error
}

func (r *Restorer) Progress() Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.progress
}

// Failures lists files whose last attempt failed, such as a body that failed authentication.
func (r *Restorer) Failures() []FileFailure {
	r.mu.Lock()
	defer r.mu.Unlock()
	failures := make([]FileFailure, 0, len(r.failures))
	for id, failed := range r.failures {
		failures = append(failures, FileFailure{ID: id, Err: failed.err})
	}
	slices.SortFunc(failures, func(a, b FileFailure) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return failures
}

func (r *Restorer) startProgress(plan roundPlan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = Progress{FilesDone: plan.doneFiles, FilesTotal: plan.totalFiles, BytesDone: plan.doneBytes,
		BytesTotal: plan.totalBytes, FilesFailed: len(r.failures), Paused: r.paused}
}

func (r *Restorer) fileDone(object index.Object) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress.FilesDone++
	r.progress.BytesDone += object.Size
	delete(r.failures, object.ID)
}

func (r *Restorer) recordFailure(id model.ObjectID, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures[id] = failure{err: err, at: time.Now()}
}

func (r *Restorer) finishProgress() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress.FilesFailed = len(r.failures)
	r.progress.Paused = r.paused
	r.progress.Complete = !r.paused && r.progress.FilesDone == r.progress.FilesTotal
}
