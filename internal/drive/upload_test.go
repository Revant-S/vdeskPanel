package drive

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"testing"

	"vault/internal/drive/drivefake"
)

func matchKind(kind drivefake.RequestKind) func(*http.Request) bool {
	return func(r *http.Request) bool { return drivefake.KindOf(r) == kind }
}

const uploadTestSize = 4*testChunkSize + 1000

func startTestUpload(t *testing.T, client *Client, size int64) UploadSession {
	t.Helper()
	ids, err := client.GenerateIDs(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.StartResumableUpload(context.Background(), NewFile{ID: ids[0], Name: "big", Properties: map[string]string{"oid": "o1"}}, size)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestResumableUploadSendsChunksAndConfirmsOffsets(t *testing.T) {
	server, client := newFakeAndClient(t)
	content := randomContent(t, uploadTestSize)
	session := startTestUpload(t, client, uploadTestSize)
	var confirmed []int64
	file, err := client.ResumeUpload(context.Background(), session, bytes.NewReader(content), func(offset int64) {
		confirmed = append(confirmed, offset)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{testChunkSize, 2 * testChunkSize, 3 * testChunkSize, 4 * testChunkSize}
	if !slices.Equal(confirmed, want) {
		t.Fatalf("confirmed %v, want %v", confirmed, want)
	}
	sum := md5.Sum(content)
	if file.MD5 != hex.EncodeToString(sum[:]) || file.Size != uploadTestSize || file.Properties["oid"] != "o1" {
		t.Fatalf("uploaded file = %+v", file)
	}
	if _, stored, _ := server.File(file.ID); !bytes.Equal(stored, content) {
		t.Fatal("stored content differs")
	}
}

func TestResumableUploadCutMidChunkResumesFromConfirmedOffset(t *testing.T) {
	server, client := newFakeAndClient(t)
	content := randomContent(t, uploadTestSize)
	session := startTestUpload(t, client, uploadTestSize)
	const cutAfter = 100_000
	cutAt := int64(testChunkSize + cutAfter)
	server.InjectFault(drivefake.Fault{
		Match: func(r *http.Request) bool {
			return r.Method == http.MethodPut && r.Header.Get("Content-Range") == "bytes 262144-524287/1049576"
		},
		Status: http.StatusServiceUnavailable, Reason: "backendError", AfterBytes: cutAfter,
	})
	var confirmed []int64
	file, err := client.ResumeUpload(context.Background(), session, bytes.NewReader(content), func(offset int64) {
		confirmed = append(confirmed, offset)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(confirmed, cutAt) {
		t.Fatalf("confirmed %v never reported the cut offset %d", confirmed, cutAt)
	}
	if _, stored, _ := server.File(file.ID); !bytes.Equal(stored, content) {
		t.Fatal("stored content differs after resume")
	}
	assertNoByteResentBelow(t, server.Requests(), cutAt)
}

// assertNoByteResentBelow checks that, after the cut chunk, every chunk the fake received
// started at or beyond the offset it had already persisted.
func assertNoByteResentBelow(t *testing.T, records []drivefake.RequestRecord, persisted int64) {
	t.Helper()
	cutIndex := slices.IndexFunc(records, func(record drivefake.RequestRecord) bool {
		return record.Kind == drivefake.KindUpload && record.Status == http.StatusServiceUnavailable
	})
	if cutIndex < 0 {
		t.Fatal("the cut never happened")
	}
	if records[cutIndex+1].Offset != -1 {
		t.Fatalf("first request after the cut sent content from %d instead of asking for status", records[cutIndex+1].Offset)
	}
	for _, record := range records[cutIndex+1:] {
		if record.Offset >= 0 && record.Offset < persisted {
			t.Fatalf("re-sent bytes from offset %d, below the persisted %d", record.Offset, persisted)
		}
	}
}

func TestResumeUploadAfterRestartAsksForStatusFirst(t *testing.T) {
	server, client := newFakeAndClient(t)
	content := randomContent(t, uploadTestSize)
	session := startTestUpload(t, client, uploadTestSize)
	ctx, crash := context.WithCancel(context.Background())
	_, err := client.ResumeUpload(ctx, session, bytes.NewReader(content), func(offset int64) {
		if offset >= 2*testChunkSize {
			crash()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("crashed upload: %v, want context.Canceled", err)
	}
	restarted := newTestClient(t, server, Deps{})
	before := len(server.Requests())
	file, err := restarted.ResumeUpload(context.Background(), session, bytes.NewReader(content), nil)
	if err != nil {
		t.Fatal(err)
	}
	records := server.Requests()[before:]
	if records[0].Offset != -1 || records[1].Offset != 2*testChunkSize {
		t.Fatalf("restart sent offsets %d then %d, want a status query then %d", records[0].Offset, records[1].Offset, 2*testChunkSize)
	}
	if _, stored, _ := server.File(file.ID); !bytes.Equal(stored, content) {
		t.Fatal("stored content differs after restart")
	}
}

func TestExpiredUploadSessionIsReported(t *testing.T) {
	server, client := newFakeAndClient(t)
	content := randomContent(t, uploadTestSize)
	session := startTestUpload(t, client, uploadTestSize)
	server.ExpireUploadSessions()
	if _, err := client.ResumeUpload(context.Background(), session, bytes.NewReader(content), nil); !errors.Is(err, ErrUploadSessionExpired) {
		t.Fatalf("got %v, want ErrUploadSessionExpired", err)
	}

	gone := startTestUpload(t, client, uploadTestSize)
	server.InjectFault(drivefake.Fault{Match: matchKind(drivefake.KindUpload), Status: http.StatusGone, Reason: "gone"})
	if _, err := client.ResumeUpload(context.Background(), gone, bytes.NewReader(content), nil); !errors.Is(err, ErrUploadSessionExpired) {
		t.Fatalf("410: got %v, want ErrUploadSessionExpired", err)
	}
}

func TestResumeUploadOfFinishedSessionReturnsTheFile(t *testing.T) {
	_, client := newFakeAndClient(t)
	content := randomContent(t, uploadTestSize)
	session := startTestUpload(t, client, uploadTestSize)
	first, err := client.ResumeUpload(context.Background(), session, bytes.NewReader(content), nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := client.ResumeUpload(context.Background(), session, bytes.NewReader(content), nil)
	if err != nil || again.ID != first.ID || again.MD5 != first.MD5 {
		t.Fatalf("replayed resume = %+v, %v", again, err)
	}
}
