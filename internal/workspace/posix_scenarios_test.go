package workspace

import (
	"bytes"
	"crypto/rand"
	mathrand "math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

const segment = 64 << 10

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	data := make([]byte, n)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	return data
}

// checkFile compares what the mount, the tree and the index say about path with want.
func checkFile(t *testing.T, home *testHome, root, path string, want []byte) {
	t.Helper()
	full := filepath.Join(root, path)
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: read %d bytes, want %d; first difference at %d", path, len(got), len(want), firstDifference(got, want))
	}
	info, err := os.Stat(full)
	if err != nil || info.Size() != int64(len(want)) {
		t.Fatalf("%s: stat %v, %v; want size %d", path, info, err, len(want))
	}
	// The kernel sends release after close returns, so the index catches up shortly after.
	id := oidAt(t, home, "/"+path)
	deadline := time.Now().Add(2 * time.Second)
	for {
		object := home.object(t, id)
		if object.Size == int64(len(want)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: index size %d, want %d", path, object.Size, len(want))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func firstDifference(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

func TestFileSizesAroundSegmentBoundaries(t *testing.T) {
	home, root := mountHome(t)
	for _, size := range []int{0, 1, segment - 1, segment, segment + 1, 2 * segment, 3*segment + 5} {
		name := "size-" + strconv.Itoa(size)
		data := randomBytes(t, size)
		must(t, os.WriteFile(filepath.Join(root, name), data, 0o644))
		checkFile(t, home, root, name, data)
	}
}

func TestRandomWritesAndTruncatesMatchAModel(t *testing.T) {
	home, root := mountHome(t)
	path := filepath.Join(root, "model.bin")
	random := mathrand.New(mathrand.NewPCG(1, 2))
	var model []byte
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	must(t, err)
	for step := range 200 {
		switch random.IntN(4) {
		case 0:
			size := random.IntN(5 * segment)
			must(t, file.Truncate(int64(size)))
			if size < len(model) {
				model = model[:size]
			} else {
				model = append(model, make([]byte, size-len(model))...)
			}
		default:
			offset := random.IntN(5 * segment)
			length := random.IntN(2*segment) + 1
			chunk := randomBytes(t, length)
			if _, err := file.WriteAt(chunk, int64(offset)); err != nil {
				t.Fatalf("step %d: %v", step, err)
			}
			if end := offset + length; end > len(model) {
				model = append(model, make([]byte, end-len(model))...)
			}
			copy(model[offset:], chunk)
		}
		if step%20 == 0 {
			got := make([]byte, len(model)+10)
			n, _ := file.ReadAt(got, 0)
			if !bytes.Equal(got[:n], model) {
				t.Fatalf("step %d: read %d bytes, want %d, first difference at %d", step, n, len(model), firstDifference(got[:n], model))
			}
		}
	}
	must(t, file.Close())
	checkFile(t, home, root, "model.bin", model)
}

func TestConcurrentWritersOnOneDescriptor(t *testing.T) {
	home, root := mountHome(t)
	path := filepath.Join(root, "parallel.bin")
	const chunks, chunkSize = 64, 20_000
	want := randomBytes(t, chunks*chunkSize)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	must(t, err)
	var wg sync.WaitGroup
	for _, chunk := range mathrand.Perm(chunks) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			offset := chunk * chunkSize
			if _, err := file.WriteAt(want[offset:offset+chunkSize], int64(offset)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	must(t, file.Close())
	checkFile(t, home, root, "parallel.bin", want)
}

func TestConcurrentWritersOnSeparateDescriptors(t *testing.T) {
	home, root := mountHome(t)
	path := filepath.Join(root, "shared.bin")
	must(t, os.WriteFile(path, nil, 0o644))
	const writers, chunkSize = 16, 50_000
	want := randomBytes(t, writers*chunkSize)
	var wg sync.WaitGroup
	for writer := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			file, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			offset := writer * chunkSize
			if _, err := file.WriteAt(want[offset:offset+chunkSize], int64(offset)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	checkFile(t, home, root, "shared.bin", want)
}

func TestRenameWhileOpenForWriting(t *testing.T) {
	home, root := mountHome(t)
	must(t, os.Mkdir(filepath.Join(root, "Done"), 0o755))
	first, second := randomBytes(t, 70_000), randomBytes(t, 90_000)
	file, err := os.Create(filepath.Join(root, "download.part"))
	must(t, err)
	_, err = file.Write(first)
	must(t, err)
	must(t, os.Rename(filepath.Join(root, "download.part"), filepath.Join(root, "Done", "movie.mkv")))
	_, err = file.Write(second)
	must(t, err)
	must(t, file.Close())
	checkFile(t, home, root, "Done/movie.mkv", append(first, second...))
}

func TestAppendFromTwoDescriptors(t *testing.T) {
	home, root := mountHome(t)
	path := filepath.Join(root, "log.txt")
	a, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	must(t, err)
	b, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	must(t, err)
	var want []byte
	for i := range 50 {
		line := []byte("line " + strconv.Itoa(i) + "\n")
		target := a
		if i%2 == 1 {
			target = b
		}
		_, err := target.Write(line)
		must(t, err)
		want = append(want, line...)
	}
	must(t, a.Close())
	must(t, b.Close())
	checkFile(t, home, root, "log.txt", want)
}

func TestTruncateUpReadsZerosAndOTruncEmpties(t *testing.T) {
	home, root := mountHome(t)
	path := filepath.Join(root, "grow.bin")
	head := randomBytes(t, 100)
	must(t, os.WriteFile(path, head, 0o644))
	must(t, os.Truncate(path, 2*segment+7))
	checkFile(t, home, root, "grow.bin", append(head, make([]byte, 2*segment+7-100)...))
	must(t, os.WriteFile(path, []byte("short"), 0o644))
	checkFile(t, home, root, "grow.bin", []byte("short"))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	must(t, err)
	must(t, file.Close())
	checkFile(t, home, root, "grow.bin", nil)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
