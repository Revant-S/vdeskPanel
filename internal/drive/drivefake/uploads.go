package drivefake

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	drive "google.golang.org/api/drive/v3"
)

const (
	maxMultipartContent = 5 << 20
	chunkAlignment      = 256 << 10
)

type uploadSession struct {
	metadata *drive.File
	size     int64
	received []byte
	fields   string
	expired  bool
	fileID   string
}

func (s *Server) startUpload(w http.ResponseWriter, r *http.Request) *apiError {
	switch uploadType := r.URL.Query().Get("uploadType"); uploadType {
	case "multipart":
		return s.createMultipart(w, r)
	case "resumable":
		return s.openSession(w, r)
	default:
		return badRequest("unsupported uploadType " + uploadType)
	}
}

func (s *Server) createMultipart(w http.ResponseWriter, r *http.Request) *apiError {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/related" {
		return badRequest("multipart upload needs multipart/related")
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	metadataPart, err := reader.NextPart()
	if err != nil {
		return badRequest("missing metadata part")
	}
	metadata, apiErr := decodeMetadata(metadataPart)
	if apiErr != nil {
		return apiErr
	}
	mediaPart, err := reader.NextPart()
	if err != nil {
		return badRequest("missing media part")
	}
	content, err := io.ReadAll(io.LimitReader(mediaPart, maxMultipartContent+1))
	if err != nil {
		return badRequest("unreadable media part")
	}
	if len(content) > maxMultipartContent {
		return badRequest("multipart content exceeds 5 MiB; use a resumable upload")
	}
	file, apiErr := s.store.insert(metadata, content)
	if apiErr != nil {
		return apiErr
	}
	writeMasked(w, http.StatusOK, file.toDrive(), r.URL.Query().Get("fields"), defaultFileMask)
	return nil
}

func (s *Server) openSession(w http.ResponseWriter, r *http.Request) *apiError {
	metadata, apiErr := decodeMetadata(r.Body)
	if apiErr != nil {
		return apiErr
	}
	size, err := strconv.ParseInt(r.Header.Get("X-Upload-Content-Length"), 10, 64)
	if err != nil || size < 0 {
		return badRequest("X-Upload-Content-Length is required")
	}
	if apiErr := s.store.checkParents(metadata.Parents); apiErr != nil {
		return apiErr
	}
	if _, exists := s.store.files[metadata.Id]; exists {
		return failure(http.StatusConflict, "duplicate", "A file already exists with the provided ID.")
	}
	sessionID := s.store.generateID()
	s.store.sessions[sessionID] = &uploadSession{metadata: metadata, size: size, fields: r.URL.Query().Get("fields")}
	w.Header().Set("Location", "http://"+r.Host+"/upload/drive/v3/files?uploadType=resumable&upload_id="+sessionID)
	w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) receiveUploadChunk(w http.ResponseWriter, r *http.Request) *apiError {
	session, ok := s.store.sessions[r.URL.Query().Get("upload_id")]
	if !ok || session.expired {
		return failure(http.StatusNotFound, "notFound", "Upload session not found or expired.")
	}
	if session.fileID != "" {
		return s.answerCompleted(w, session)
	}
	start, end, total, apiErr := parseContentRange(r.Header.Get("Content-Range"))
	if apiErr != nil {
		return apiErr
	}
	if total != session.size {
		return badRequest("Content-Range total does not match the session size")
	}
	if start >= 0 {
		if apiErr := s.appendChunk(r, session, start, end); apiErr != nil {
			return apiErr
		}
	}
	if int64(len(session.received)) < session.size {
		if len(session.received) > 0 {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(session.received)-1))
		}
		w.WriteHeader(http.StatusPermanentRedirect)
		return nil
	}
	file, apiErr := s.store.insert(session.metadata, session.received)
	if apiErr != nil {
		return apiErr
	}
	session.fileID = file.id
	return s.answerCompleted(w, session)
}

// appendChunk is strict about offsets: a chunk must start exactly at the persisted byte
// count, so a client that re-sends confirmed bytes fails loudly.
func (s *Server) appendChunk(r *http.Request, session *uploadSession, start, end int64) *apiError {
	if start != int64(len(session.received)) {
		return badRequest(fmt.Sprintf("chunk starts at %d but %d bytes are persisted", start, len(session.received)))
	}
	length := end - start + 1
	if end+1 < session.size && length%chunkAlignment != 0 {
		return badRequest("non-final chunk is not a multiple of 256 KiB")
	}
	if cut := cutFrom(r); cut != nil {
		kept, _ := io.ReadAll(io.LimitReader(r.Body, min(cut.AfterBytes, length)))
		session.received = append(session.received, kept...)
		return failure(cut.Status, cut.Reason, "injected fault after partial upload")
	}
	chunk, err := io.ReadAll(io.LimitReader(r.Body, length))
	session.received = append(session.received, chunk...)
	if err != nil || int64(len(chunk)) != length {
		return badRequest("chunk body is shorter than its Content-Range")
	}
	return nil
}

func (s *Server) answerCompleted(w http.ResponseWriter, session *uploadSession) *apiError {
	file, apiErr := s.lookup(session.fileID)
	if apiErr != nil {
		return apiErr
	}
	writeMasked(w, http.StatusOK, file.toDrive(), session.fields, defaultFileMask)
	return nil
}

// parseContentRange reads "bytes a-b/N" or the status query "bytes */N" (start -1).
func parseContentRange(header string) (int64, int64, int64, *apiError) {
	invalid := badRequest("invalid Content-Range " + header)
	spec, found := strings.CutPrefix(header, "bytes ")
	byteRange, rawTotal, hasTotal := strings.Cut(spec, "/")
	if !found || !hasTotal {
		return 0, 0, 0, invalid
	}
	total, err := strconv.ParseInt(rawTotal, 10, 64)
	if err != nil {
		return 0, 0, 0, invalid
	}
	if byteRange == "*" {
		return -1, -1, total, nil
	}
	first, last, hasDash := strings.Cut(byteRange, "-")
	start, startErr := strconv.ParseInt(first, 10, 64)
	end, endErr := strconv.ParseInt(last, 10, 64)
	if !hasDash || startErr != nil || endErr != nil || end < start || end >= total {
		return 0, 0, 0, invalid
	}
	return start, end, total, nil
}
