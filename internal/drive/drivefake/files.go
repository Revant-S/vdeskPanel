package drivefake

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	drive "google.golang.org/api/drive/v3"
)

const (
	defaultPageSize     = 100
	maxPageSize         = 1000
	defaultGeneratedIDs = 10
)

type apiHandler func(http.ResponseWriter, *http.Request) *apiError

func (s *Server) registerRoutes() {
	routes := map[string]apiHandler{
		"GET /drive/v3/files/generateIds":      s.generateIDs,
		"POST /drive/v3/files":                 s.createMetadata,
		"GET /drive/v3/files":                  s.listFiles,
		"GET /drive/v3/files/{id}":             s.getFile,
		"PATCH /drive/v3/files/{id}":           s.updateFile,
		"DELETE /drive/v3/files/{id}":          s.deleteFile,
		"POST /drive/v3/files/{id}/copy":       s.copyFile,
		"POST /upload/drive/v3/files":          s.startUpload,
		"PUT /upload/drive/v3/files":           s.receiveUploadChunk,
		"GET /drive/v3/changes/startPageToken": s.startPageToken,
		"GET /drive/v3/changes":                s.listChanges,
	}
	for pattern, handler := range routes {
		s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if err := handler(w, r); err != nil {
				writeError(w, err)
			}
		})
	}
}

func decodeMetadata(body io.Reader) (*drive.File, *apiError) {
	var metadata drive.File
	if err := json.NewDecoder(body).Decode(&metadata); err != nil && err != io.EOF {
		return nil, badRequest("invalid file metadata: " + err.Error())
	}
	return &metadata, nil
}

func (s *Server) lookup(id string) (*storedFile, *apiError) {
	file, ok := s.store.files[id]
	if !ok {
		return nil, notFound(id)
	}
	return file, nil
}

func (s *Server) generateIDs(w http.ResponseWriter, r *http.Request) *apiError {
	query := r.URL.Query()
	count := defaultGeneratedIDs
	if raw := query.Get("count"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxPageSize {
			return badRequest("count must be between 1 and 1000")
		}
		count = parsed
	}
	if space := query.Get("space"); space != "" && space != "drive" {
		return badRequest("unsupported space " + space)
	}
	ids := s.store.issueIDs(count)
	writeJSON(w, http.StatusOK, &drive.GeneratedIds{Kind: "drive#generatedIds", Space: "drive", Ids: ids})
	return nil
}

func (s *Server) createMetadata(w http.ResponseWriter, r *http.Request) *apiError {
	metadata, err := decodeMetadata(r.Body)
	if err != nil {
		return err
	}
	file, err := s.store.insert(metadata, nil)
	if err != nil {
		return err
	}
	writeMasked(w, http.StatusOK, file.toDrive(), r.URL.Query().Get("fields"), defaultFileMask)
	return nil
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) *apiError {
	file, err := s.lookup(r.PathValue("id"))
	if err != nil {
		return err
	}
	if r.URL.Query().Get("alt") == "media" {
		return serveMedia(w, r, file)
	}
	writeMasked(w, http.StatusOK, file.toDrive(), r.URL.Query().Get("fields"), defaultFileMask)
	return nil
}

func (s *Server) updateFile(w http.ResponseWriter, r *http.Request) *apiError {
	file, err := s.lookup(r.PathValue("id"))
	if err != nil {
		return err
	}
	patch, err := decodeMetadata(r.Body)
	if err != nil {
		return err
	}
	query := r.URL.Query()
	added := splitIDs(query.Get("addParents"))
	if err := s.store.checkParents(added); err != nil {
		return err
	}
	if patch.Name != "" {
		file.name = patch.Name
	}
	if patch.ModifiedTime != "" {
		file.modified = parseTimeOr(patch.ModifiedTime, file.modified)
	}
	if len(patch.AppProperties) > 0 {
		if file.properties == nil {
			file.properties = map[string]string{}
		}
		maps.Copy(file.properties, patch.AppProperties)
	}
	removed := splitIDs(query.Get("removeParents"))
	file.parents = slices.DeleteFunc(file.parents, func(id string) bool { return slices.Contains(removed, id) })
	for _, parent := range added {
		if !file.hasParent(parent) {
			file.parents = append(file.parents, parent)
		}
	}
	s.store.recordChange(file)
	writeMasked(w, http.StatusOK, file.toDrive(), query.Get("fields"), defaultFileMask)
	return nil
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) *apiError {
	file, err := s.lookup(r.PathValue("id"))
	if err != nil {
		return err
	}
	s.store.remove(file.id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) copyFile(w http.ResponseWriter, r *http.Request) *apiError {
	source, err := s.lookup(r.PathValue("id"))
	if err != nil {
		return err
	}
	if source.isFolder() {
		return failure(http.StatusForbidden, "cannotCopyFile", "Folders cannot be copied.")
	}
	metadata, err := decodeMetadata(r.Body)
	if err != nil {
		return err
	}
	if metadata.Name == "" {
		metadata.Name = "Copy of " + source.name
	}
	if len(metadata.Parents) == 0 {
		metadata.Parents = source.parents
	}
	metadata.MimeType = source.mimeType
	if metadata.AppProperties == nil {
		metadata.AppProperties = source.properties
	}
	file, err := s.store.insert(metadata, slices.Clone(source.content))
	if err != nil {
		return err
	}
	writeMasked(w, http.StatusOK, file.toDrive(), r.URL.Query().Get("fields"), defaultFileMask)
	return nil
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) *apiError {
	query := r.URL.Query()
	matches := func(*storedFile) bool { return true }
	if q := query.Get("q"); q != "" {
		predicate, err := parseQuery(q)
		if err != nil {
			return badRequest(err.Error())
		}
		matches = predicate
	}
	pageSize, offset, err := parsePaging(query.Get("pageSize"), query.Get("pageToken"))
	if err != nil {
		return err
	}
	var found []*storedFile
	for _, file := range s.store.files {
		if matches(file) {
			found = append(found, file)
		}
	}
	slices.SortFunc(found, func(a, b *storedFile) int { return strings.Compare(a.id, b.id) })
	list := &drive.FileList{Kind: "drive#fileList", Files: []*drive.File{}}
	for _, file := range found[min(offset, len(found)):min(offset+pageSize, len(found))] {
		list.Files = append(list.Files, file.toDrive())
	}
	if offset+pageSize < len(found) {
		list.NextPageToken = strconv.Itoa(offset + pageSize)
	}
	writeMasked(w, http.StatusOK, list, query.Get("fields"), defaultListMask)
	return nil
}

func parsePaging(rawSize, rawToken string) (int, int, *apiError) {
	pageSize := defaultPageSize
	if rawSize != "" {
		parsed, err := strconv.Atoi(rawSize)
		if err != nil || parsed < 1 {
			return 0, 0, badRequest("invalid pageSize")
		}
		pageSize = min(parsed, maxPageSize)
	}
	offset := 0
	if rawToken != "" {
		parsed, err := strconv.Atoi(rawToken)
		if err != nil || parsed < 0 {
			return 0, 0, badRequest("invalid pageToken")
		}
		offset = parsed
	}
	return pageSize, offset, nil
}

func splitIDs(joined string) []string {
	if joined == "" {
		return nil
	}
	return strings.Split(joined, ",")
}
