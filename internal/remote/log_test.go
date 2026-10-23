package remote_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"testing"

	"vault/internal/devicetest"
	"vault/internal/drive"
	"vault/internal/drive/drivefake"
	"vault/internal/journal"
	"vault/internal/remote"
)

// lyingTransport makes Drive report a wrong checksum for every upload.
type lyingTransport struct {
	next http.RoundTripper
}

var md5Field = regexp.MustCompile(`"md5Checksum":\s*"[0-9a-f]*"`)

func (l *lyingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := l.next.RoundTrip(request)
	if err != nil || request.Method != http.MethodPost {
		return response, err
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	body = md5Field.ReplaceAll(body, []byte(`"md5Checksum": "00000000000000000000000000000000"`))
	response.Body, response.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
	response.Header.Del("Content-Length")
	return response, nil
}

func TestLogUploadDrivesStoredWronglyIsRefusedAndRemoved(t *testing.T) {
	ctx := context.Background()
	fake := drivefake.New()
	config := devicetest.FastDrive()
	config.Endpoint, config.UploadEndpoint = fake.Endpoint(), fake.UploadEndpoint()
	httpClient := fake.HTTPClient()
	httpClient.Transport = &lyingTransport{next: httpClient.Transport}
	client, err := drive.NewClient(ctx, config, drive.Deps{HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	folder, err := client.CreateFolder(ctx, drive.NewFile{Name: "log", ParentID: "root"})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := client.GenerateIDs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	log := &remote.Log{Client: client}
	if _, err := log.CreateFile(ctx, journal.LogFile{DriveID: ids[0], FolderID: folder.ID, Name: "c.vlog", Data: []byte("sealed commit")}); err == nil {
		t.Fatal("a wrongly stored commit was accepted")
	}
	if _, _, exists := fake.File(ids[0]); exists {
		t.Fatal("the wrongly stored commit was left on Drive")
	}
}
