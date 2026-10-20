package drivefake

import (
	"net/http"
	"strconv"

	drive "google.golang.org/api/drive/v3"
)

// Page tokens are positions in the change log. ExpirePageTokens moves the first valid
// position forward, and older tokens then get the 404 Drive gives for expired tokens.

func (s *Server) startPageToken(w http.ResponseWriter, _ *http.Request) *apiError {
	token := strconv.FormatInt(s.store.nextToken(), 10)
	writeJSON(w, http.StatusOK, &drive.StartPageToken{Kind: "drive#startPageToken", StartPageToken: token})
	return nil
}

func (s *Server) listChanges(w http.ResponseWriter, r *http.Request) *apiError {
	query := r.URL.Query()
	position, err := strconv.ParseInt(query.Get("pageToken"), 10, 64)
	if err != nil || position < 0 || position > s.store.nextToken() {
		return badRequest("Invalid Value")
	}
	if position < s.store.firstToken {
		return failure(http.StatusNotFound, "notFound", "Page token expired.")
	}
	pageSize, _, apiErr := parsePaging(query.Get("pageSize"), "")
	if apiErr != nil {
		return apiErr
	}
	end := min(position+int64(pageSize), s.store.nextToken())
	list := &drive.ChangeList{Kind: "drive#changeList", Changes: []*drive.Change{}}
	for _, record := range s.store.changes[position:end] {
		if record.removed && query.Get("includeRemoved") == "false" {
			continue
		}
		change := &drive.Change{Kind: "drive#change", ChangeType: "file", FileId: record.fileID, Removed: record.removed, File: record.file}
		list.Changes = append(list.Changes, change)
	}
	if end < s.store.nextToken() {
		list.NextPageToken = strconv.FormatInt(end, 10)
	} else {
		list.NewStartPageToken = strconv.FormatInt(end, 10)
	}
	writeMasked(w, http.StatusOK, list, query.Get("fields"), defaultChangesMask)
	return nil
}
