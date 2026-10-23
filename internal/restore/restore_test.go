package restore_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vault/internal/drive/drivefake"
	"vault/internal/model"
	"vault/internal/restore"
	"vault/internal/vaultfile"
)

func TestRestoreEverythingBringsBackEveryBody(t *testing.T) {
	t.Parallel()
	f := newSyncedFixture(t)
	f.forgetBodies()
	from := len(f.network.Drive.Requests())
	r := f.newRestorer(restore.Config{}, nil)
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.assertAllLocalAndIdentical()
	progress := r.Progress()
	if !progress.Complete || progress.FilesDone != len(f.files) || progress.FilesTotal != len(f.files) || progress.BytesDone != progress.BytesTotal {
		t.Fatalf("progress = %+v", progress)
	}
	large := f.downloadedRanges(f.object("/media/movie.mkv").DriveID, from)
	runBytes := int64(128 * vaultfile.SegmentCiphertextSize)
	if len(large) < 4 {
		t.Fatalf("large file came in %d requests, want header plus one per 8 MiB run", len(large))
	}
	for _, got := range large {
		if got.end-got.start > runBytes {
			t.Fatalf("range %v is longer than one 8 MiB run", got)
		}
	}
	assertNoOverlap(t, large)
}

func TestRestoreResumesAfterKillWithoutRedownloading(t *testing.T) {
	t.Parallel()
	f := newSyncedFixture(t)
	f.forgetBodies()
	from := len(f.network.Drive.Requests())
	largeDriveID := f.object("/media/movie.mkv").DriveID
	ctx, kill := context.WithCancel(context.Background())
	var segmentRequests int
	f.network.Drive.InjectFault(drivefake.Fault{Match: func(r *http.Request) bool {
		if drivefake.KindOf(r) == drivefake.KindDownload && strings.HasSuffix(r.URL.Path, "/"+largeDriveID) &&
			!strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") {
			if segmentRequests++; segmentRequests == 2 {
				kill()
			}
		}
		return false
	}})
	err := f.newRestorer(restore.Config{Files: 1, RangesPerFile: 1}, nil).RunOnce(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("killed restore = %v, want context.Canceled", err)
	}
	f.network.Drive.ClearFaults()
	if f.state("/media/movie.mkv") == model.Local {
		t.Fatal("large file finished before the kill; the test proves nothing")
	}
	f.device.Restart()

	r := f.newRestorer(restore.Config{}, nil)
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.assertAllLocalAndIdentical()
	for path := range f.files {
		assertNoOverlap(t, f.downloadedRanges(f.object(path).DriveID, from))
	}
}

func TestPauseSurvivesRestart(t *testing.T) {
	t.Parallel()
	f := newSyncedFixture(t)
	f.forgetBodies()
	ctx := context.Background()
	if err := f.newRestorer(restore.Config{}, nil).Pause(ctx); err != nil {
		t.Fatal(err)
	}
	f.device.Restart()
	before := f.downloads()
	r := f.newRestorer(restore.Config{}, nil)
	if !r.Progress().Paused {
		t.Fatal("restore is not paused after restart")
	}
	if err := r.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.downloads() - before; got != 0 {
		t.Fatalf("paused restore made %d downloads", got)
	}
	if err := r.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	f.assertAllLocalAndIdentical()
	if progress := r.Progress(); progress.Paused || !progress.Complete {
		t.Fatalf("progress after resume = %+v", progress)
	}
}

func TestRestoreYieldsWhileUploadsArePending(t *testing.T) {
	t.Parallel()
	f := newSyncedFixture(t)
	f.forgetBodies()
	var uploading atomic.Bool
	uploading.Store(true)
	r := f.newRestorer(restore.Config{}, uploading.Load)
	before := f.downloads()
	done := make(chan error, 1)
	go func() { done <- r.RunOnce(context.Background()) }()
	time.Sleep(100 * time.Millisecond)
	if got := f.downloads() - before; got != 0 {
		t.Fatalf("restore made %d downloads while uploads were pending", got)
	}
	uploading.Store(false)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.assertAllLocalAndIdentical()
}

func TestCorruptRemoteBodyFailsOnlyThatFile(t *testing.T) {
	t.Parallel()
	f := newSyncedFixture(t)
	f.forgetBodies()
	corrupt := f.object("/docs/report.pdf")
	f.network.Drive.CorruptContent(corrupt.DriveID, vaultfile.SegmentOffset(2)+100)
	r := f.newRestorer(restore.Config{}, nil)
	err := r.RunOnce(context.Background())
	if !errors.As(err, new(*vaultfile.SegmentError)) || !strings.Contains(err.Error(), string(corrupt.ID)) {
		t.Fatalf("restore error = %v, want a segment error naming %s", err, corrupt.ID)
	}
	failures := r.Failures()
	if len(failures) != 1 || failures[0].ID != corrupt.ID {
		t.Fatalf("failures = %v, want only %s", failures, corrupt.ID)
	}
	for path := range f.files {
		want := model.Local
		if path == "/docs/report.pdf" {
			want = model.CloudOnly
		}
		if got := f.state(path); got != want {
			t.Fatalf("%s state = %d, want %d", path, got, want)
		}
	}
	if progress := r.Progress(); progress.Complete || progress.FilesFailed != 1 || progress.FilesDone != len(f.files)-1 {
		t.Fatalf("progress = %+v", progress)
	}
}

func TestOnDemandDownloadsOnlyRequestedFiles(t *testing.T) {
	t.Parallel()
	f := newSyncedFixture(t)
	f.forgetBodies()
	var localMu sync.Mutex
	madeLocal := map[model.ObjectID]bool{}
	r, err := restore.New(context.Background(), restore.Deps{Index: f.device.Index, Cache: f.device.Cache,
		OnLocal: func(id model.ObjectID) { localMu.Lock(); madeLocal[id] = true; localMu.Unlock() }},
		restore.Config{Mode: restore.OnDemand, YieldInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- r.Run(ctx) }()
	if err := r.RequestLocal(ctx, []model.ObjectID{f.device.ObjectID("/media/sub"), f.device.ObjectID("/notes.txt")}); err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"/media/sub/a.bin": true, "/media/sub/b.bin": true, "/notes.txt": true}
	waitFor(t, func() bool {
		localMu.Lock()
		defer localMu.Unlock()
		return len(madeLocal) == len(wanted)
	})
	stop()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	for path := range f.files {
		if got, want := f.state(path) == model.Local, wanted[path]; got != want {
			t.Fatalf("%s Local = %v, want %v", path, got, want)
		}
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestKeptInCloudStaysFreedUnderRestoreEverythingAcrossRestarts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newSyncedFixture(t)
	f.forgetBodies()
	r := f.newRestorer(restore.Config{}, nil)
	if err := r.KeepInCloud(ctx, []model.ObjectID{f.device.ObjectID("/media")}); err != nil {
		t.Fatal(err)
	}
	r = f.newRestorer(restore.Config{}, nil)
	if err := r.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for path := range f.files {
		freed := strings.HasPrefix(path, "/media/")
		if got := f.state(path) == model.CloudOnly; got != freed {
			t.Fatalf("%s cloud only = %v, want %v", path, got, freed)
		}
	}
	if err := r.RequestLocal(ctx, []model.ObjectID{f.device.ObjectID("/media/sub")}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if f.state("/media/sub/a.bin") != model.Local || f.state("/media/photo.jpg") != model.CloudOnly {
		t.Fatal("downloading a folder should bring back only that folder")
	}
}
