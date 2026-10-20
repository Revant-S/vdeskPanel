package restore_test

import (
	"bytes"
	"context"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/devicetest"
	"github.com/cosmoboy/vault/internal/drive/drivefake"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
	"github.com/cosmoboy/vault/internal/restore"
)

const largeFileSize = 20<<20 + 12345

type fixture struct {
	t       *testing.T
	network *devicetest.Network
	device  *devicetest.Device
	files   map[string][]byte
}

// newSyncedFixture writes a handful of files, one larger than several 8 MiB runs, and syncs.
func newSyncedFixture(t *testing.T) *fixture {
	t.Helper()
	network := devicetest.NewNetwork(t)
	f := &fixture{t: t, network: network, device: network.NewDevice(t, "a"), files: map[string][]byte{
		"/notes.txt":        patterned(1, 3000),
		"/docs/report.pdf":  patterned(2, 300<<10),
		"/docs/empty.txt":   {},
		"/media/movie.mkv":  patterned(3, largeFileSize),
		"/media/photo.jpg":  patterned(4, 2<<20+7),
		"/media/sub/a.bin":  patterned(5, 65536),
		"/media/sub/b.bin":  patterned(6, 65537),
		"/docs/deep/c.json": patterned(7, 900),
	}}
	f.device.Mkdir("/docs")
	f.device.Mkdir("/docs/deep")
	f.device.Mkdir("/media")
	f.device.Mkdir("/media/sub")
	for path, data := range f.files {
		f.device.WriteFile(path, data)
	}
	f.device.Sync()
	return f
}

func patterned(seed uint64, size int) []byte {
	random := rand.New(rand.NewPCG(seed, seed*31))
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(random.Uint32())
	}
	return data
}

// forgetBodies makes the device look freshly joined: every body freed, every file CloudOnly.
func (f *fixture) forgetBodies() {
	f.t.Helper()
	ctx := context.Background()
	d := f.device
	for path := range f.files {
		if err := d.Cache.Free(ctx, d.ObjectID(path)); err != nil {
			f.t.Fatal(err)
		}
	}
	err := d.Index.Update(ctx, func(tx *index.Tx) error {
		for path := range f.files {
			if err := tx.SetLocalState(d.ObjectID(path), model.CloudOnly); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) newRestorer(config restore.Config, uploadsPending func() bool) *restore.Restorer {
	f.t.Helper()
	if config.YieldInterval == 0 {
		config.YieldInterval = 5 * time.Millisecond
	}
	r, err := restore.New(context.Background(), restore.Deps{Index: f.device.Index, Cache: f.device.Cache,
		UploadsPending: uploadsPending, Logger: f.network.Logger}, config)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) object(path string) index.Object {
	f.t.Helper()
	var object index.Object
	err := f.device.Index.View(context.Background(), func(tx *index.Tx) error {
		var err error
		object, err = tx.Object(f.device.ObjectID(path))
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return object
}

func (f *fixture) state(path string) model.LocalState {
	f.t.Helper()
	return f.object(path).State
}

func (f *fixture) downloads() int {
	count := 0
	for _, record := range f.network.Drive.Requests() {
		if record.Kind == drivefake.KindDownload {
			count++
		}
	}
	return count
}

// assertAllLocalAndIdentical reads every file back and checks no read needed the network.
func (f *fixture) assertAllLocalAndIdentical() {
	f.t.Helper()
	before := f.downloads()
	for path, want := range f.files {
		if state := f.state(path); state != model.Local {
			f.t.Fatalf("%s state = %d, want Local", path, state)
		}
		if got := f.device.ReadFile(path); !bytes.Equal(got, want) {
			f.t.Fatalf("%s differs after restore: %d bytes, want %d", path, len(got), len(want))
		}
	}
	if after := f.downloads(); after != before {
		f.t.Fatalf("reading restored files made %d downloads", after-before)
	}
}

type byteRange struct {
	start, end int64
}

// downloadedRanges lists the byte ranges served for one Drive file, from request seq onward.
func (f *fixture) downloadedRanges(driveID string, from int) []byteRange {
	var ranges []byteRange
	for _, record := range f.network.Drive.Requests()[from:] {
		if record.Kind == drivefake.KindDownload && strings.HasSuffix(record.Path, "/"+driveID) && record.Bytes > 0 {
			start := max(record.Offset, 0)
			ranges = append(ranges, byteRange{start, start + record.Bytes})
		}
	}
	return ranges
}

func assertNoOverlap(t *testing.T, ranges []byteRange) {
	t.Helper()
	for i, a := range ranges {
		for _, b := range ranges[i+1:] {
			if a.start < b.end && b.start < a.end {
				t.Fatalf("byte range %v downloaded again as %v", a, b)
			}
		}
	}
}
