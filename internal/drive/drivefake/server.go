// Package drivefake is an in-memory Google Drive v3 server covering the REST subset the
// drive client uses, with injectable faults and a quota model.
package drivefake

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cosmoboy/vault/internal/model"
)

// InProcessURL is the base URL that HTTPClient routes to the fake without any socket.
const InProcessURL = "http://drivefake.invalid"

type Option func(*Server)

func WithNow(now func() time.Time) Option {
	return func(s *Server) { s.now = now }
}

// WithQuota makes the fake answer like Drive under load: 403 userRateLimitExceeded beyond
// perMinute requests, 429 beyond createsPerSecond file creations.
func WithQuota(perMinute int, createsPerSecond float64, burst int) Option {
	return func(s *Server) { s.quota = newQuota(perMinute, createsPerSecond, burst) }
}

type Server struct {
	mu      sync.Mutex
	now     func() time.Time
	store   *store
	quota   *quota
	faults  []*Fault
	records []RequestRecord
	mux     *http.ServeMux
}

func New(options ...Option) *Server {
	s := &Server{now: time.Now, mux: http.NewServeMux()}
	for _, option := range options {
		option(s)
	}
	s.store = newStore(s.now)
	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler { return s }

func (s *Server) HTTPClient() *http.Client {
	return &http.Client{Transport: &inProcessTransport{handler: s}}
}

func (s *Server) Endpoint() string { return InProcessURL + "/drive/v3/" }

func (s *Server) UploadEndpoint() string { return InProcessURL + "/upload/drive/v3/" }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counted := &countingReader{reader: r.Body}
	r.Body = counted
	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	record := RequestRecord{Method: r.Method, Path: r.URL.Path, Kind: KindOf(r), Offset: requestOffset(r)}
	s.serveLocked(recorder, r)
	record.Status = recorder.status
	record.Bytes = counted.count
	if record.Kind == KindDownload {
		record.Bytes = recorder.bytes
	}
	s.records = append(s.records, record)
}

func (s *Server) serveLocked(w http.ResponseWriter, r *http.Request) {
	fault := s.takeFault(r)
	if fault != nil && fault.AfterBytes == 0 {
		writeError(w, failure(fault.Status, fault.Reason, "injected fault"))
		return
	}
	if err := s.quota.admit(r, s.now()); err != nil {
		writeError(w, err)
		return
	}
	s.mux.ServeHTTP(w, withCut(r, fault))
}

func (s *Server) Requests() []RequestRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records)
}

func (s *Server) File(id string) (model.RemoteFile, []byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, ok := s.store.files[id]
	if !ok {
		return model.RemoteFile{}, nil, false
	}
	return file.toRemote(), slices.Clone(file.content), true
}

// CorruptContent flips one stored byte but leaves md5Checksum alone, like silent damage
// that only a deep verify (decrypt every body) can find.
func (s *Server) CorruptContent(id string, offset int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if file, ok := s.store.files[id]; ok && offset >= 0 && offset < int64(len(file.content)) {
		file.content[offset] ^= 0xff
	}
}

func (s *Server) ExpirePageTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store.firstToken = s.store.nextToken()
}

func (s *Server) ExpireUploadSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, session := range s.store.sessions {
		session.expired = true
	}
}

type RequestKind string

const (
	KindMetadata RequestKind = "metadata"
	KindUpload   RequestKind = "upload"
	KindDownload RequestKind = "download"
	KindList     RequestKind = "list"
	KindChanges  RequestKind = "changes"
)

type RequestRecord struct {
	Method string
	Path   string
	Kind   RequestKind
	Status int
	// Bytes is the request body received for uploads and the content sent for downloads.
	Bytes int64
	// Offset is the first content byte of an upload chunk or download range, else -1.
	Offset int64
}

func KindOf(r *http.Request) RequestKind {
	switch {
	case strings.HasPrefix(r.URL.Path, "/upload/"):
		return KindUpload
	case r.URL.Query().Get("alt") == "media":
		return KindDownload
	case strings.HasPrefix(r.URL.Path, "/drive/v3/changes"):
		return KindChanges
	case r.Method == http.MethodGet && r.URL.Path == "/drive/v3/files":
		return KindList
	}
	return KindMetadata
}

func requestOffset(r *http.Request) int64 {
	header := r.Header.Get("Content-Range")
	prefix := "bytes "
	if KindOf(r) == KindDownload {
		header, prefix = r.Header.Get("Range"), "bytes="
	}
	start, _, found := strings.Cut(strings.TrimPrefix(header, prefix), "-")
	if !found {
		return -1
	}
	offset, err := strconv.ParseInt(start, 10, 64)
	if err != nil {
		return -1
	}
	return offset
}

type countingReader struct {
	reader io.ReadCloser
	count  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.reader == nil {
		return 0, io.EOF
	}
	n, err := c.reader.Read(p)
	c.count += int64(n)
	return n, err
}

func (c *countingReader) Close() error {
	if c.reader == nil {
		return nil
	}
	return c.reader.Close()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

type inProcessTransport struct {
	handler http.Handler
}

func (t *inProcessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	serverRequest := req.Clone(req.Context())
	if serverRequest.Body == nil {
		serverRequest.Body = http.NoBody
	}
	serverRequest.RequestURI = req.URL.RequestURI()
	serverRequest.Host = req.URL.Host
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, serverRequest)
	if req.Body != nil {
		_ = req.Body.Close()
	}
	response := recorder.Result()
	response.Request = req
	return response, nil
}
