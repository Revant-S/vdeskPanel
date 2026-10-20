package drive

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmoboy/vault/internal/drive/drivefake"
	"github.com/cosmoboy/vault/internal/model"
)

const testChunkSize = 256 << 10

type testClock struct {
	mu      sync.Mutex
	current time.Time
}

func newTestClock() *testClock {
	return &testClock{current: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *testClock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(by)
}

func testConfig(server *drivefake.Server) Config {
	return Config{
		Endpoint:       server.Endpoint(),
		UploadEndpoint: server.UploadEndpoint(),
		ChunkSize:      testChunkSize,
		Budget:         BudgetConfig{MetadataWritesPerSecond: 1e6, MetadataWriteBurst: 1e6, ReadsPerSecond: 1e6, ReadBurst: 1e6},
		Retry:          RetryConfig{BaseDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond, MaxAttempts: 8},
	}
}

func newTestClient(t *testing.T, server *drivefake.Server, deps Deps) *Client {
	t.Helper()
	deps.HTTPClient = server.HTTPClient()
	client, err := NewClient(context.Background(), testConfig(server), deps)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func newFakeAndClient(t *testing.T) (*drivefake.Server, *Client) {
	t.Helper()
	server := drivefake.New()
	return server, newTestClient(t, server, Deps{})
}

func randomContent(t *testing.T, size int) []byte {
	t.Helper()
	content := make([]byte, size)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	return content
}

func createTestFolder(t *testing.T, client *Client, parentID, name string) model.RemoteFile {
	t.Helper()
	folder, err := client.CreateFolder(context.Background(), NewFile{Name: name, ParentID: parentID, Properties: map[string]string{"vid": "v1"}})
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	return folder
}

func createTestFile(t *testing.T, client *Client, parentID string, content []byte) model.RemoteFile {
	t.Helper()
	file, err := client.CreateSmallFile(context.Background(), NewFile{Name: "body", ParentID: parentID}, bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	return file
}

func matchMethodAndPath(method, pathSuffix string) func(*http.Request) bool {
	return func(r *http.Request) bool { return r.Method == method && strings.HasSuffix(r.URL.Path, pathSuffix) }
}

func countRequests(server *drivefake.Server, kind drivefake.RequestKind) int {
	count := 0
	for _, record := range server.Requests() {
		if record.Kind == kind {
			count++
		}
	}
	return count
}

type countingRefresher struct {
	mu    sync.Mutex
	calls int
}

func (r *countingRefresher) RefreshToken(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return nil
}
