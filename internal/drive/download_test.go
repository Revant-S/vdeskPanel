package drive

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"vault/internal/drive/drivefake"
)

func readBack(t *testing.T, file *os.File, offset, length int64) []byte {
	t.Helper()
	buf := make([]byte, length)
	if _, err := file.ReadAt(buf, offset); err != nil {
		t.Fatal(err)
	}
	return buf
}

func newSparseFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func TestDownloadRangeOfTheMiddle(t *testing.T) {
	server, client := newFakeAndClient(t)
	content := randomContent(t, 1_000_000)
	session := startTestUpload(t, client, int64(len(content)))
	remote, err := client.ResumeUpload(context.Background(), session, bytes.NewReader(content), nil)
	if err != nil {
		t.Fatal(err)
	}
	target := newSparseFile(t)
	before := len(server.Requests())
	if err := client.DownloadRange(context.Background(), remote.ID, 300_000, 200_000, target); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readBack(t, target, 300_000, 200_000), content[300_000:500_000]) {
		t.Fatal("downloaded range differs")
	}
	record := server.Requests()[before]
	if record.Kind != drivefake.KindDownload || record.Status != http.StatusPartialContent || record.Offset != 300_000 || record.Bytes != 200_000 {
		t.Fatalf("download request = %+v", record)
	}
}

func TestDownloadRangeResumesAfterCut(t *testing.T) {
	server, client := newFakeAndClient(t)
	content := randomContent(t, 100_000)
	remote := createTestFile(t, client, "", content)
	server.InjectFault(drivefake.Fault{Match: matchKind(drivefake.KindDownload), Status: http.StatusOK, AfterBytes: 5_000})
	target := newSparseFile(t)
	before := len(server.Requests())
	if err := client.DownloadRange(context.Background(), remote.ID, 10_000, 50_000, target); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readBack(t, target, 10_000, 50_000), content[10_000:60_000]) {
		t.Fatal("resumed range differs")
	}
	records := server.Requests()[before:]
	if len(records) != 2 || records[1].Offset != 15_000 {
		t.Fatalf("requests %+v, want a resume from 15000", records)
	}
}

func TestDownloadAllResumesAfterCut(t *testing.T) {
	server, client := newFakeAndClient(t)
	content := randomContent(t, 300_000)
	remote := createTestFile(t, client, "", content)
	server.InjectFault(drivefake.Fault{Match: matchKind(drivefake.KindDownload), Status: http.StatusOK, AfterBytes: 123_456})
	var downloaded bytes.Buffer
	if err := client.DownloadAll(context.Background(), remote.ID, &downloaded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(downloaded.Bytes(), content) {
		t.Fatal("downloaded content differs")
	}
}

func TestDownloadMissingFileIsNotFound(t *testing.T) {
	_, client := newFakeAndClient(t)
	err := client.DownloadAll(context.Background(), "missing", &bytes.Buffer{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
