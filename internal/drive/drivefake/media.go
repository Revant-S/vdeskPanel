package drivefake

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func serveMedia(w http.ResponseWriter, r *http.Request, file *storedFile) *apiError {
	if file.isFolder() {
		return failure(http.StatusForbidden, "fileNotDownloadable", "Only files with binary content can be downloaded.")
	}
	size := int64(len(file.content))
	start, end, status := int64(0), size-1, http.StatusOK
	if header := r.Header.Get("Range"); header != "" {
		var err *apiError
		start, end, err = parseByteRange(header, size)
		if err != nil {
			return err
		}
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	}
	body := file.content[start : end+1]
	w.Header().Set("Content-Type", file.mimeType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if cut := cutFrom(r); cut != nil && cut.AfterBytes < int64(len(body)) {
		body = body[:cut.AfterBytes]
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return nil
}

func parseByteRange(header string, size int64) (int64, int64, *apiError) {
	spec, found := strings.CutPrefix(header, "bytes=")
	first, last, hasDash := strings.Cut(spec, "-")
	if !found || !hasDash {
		return 0, 0, badRequest("unsupported Range header " + header)
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, badRequest("unsupported Range header " + header)
	}
	end := size - 1
	if last != "" {
		end, err = strconv.ParseInt(last, 10, 64)
		if err != nil || end < start {
			return 0, 0, badRequest("unsupported Range header " + header)
		}
		end = min(end, size-1)
	}
	if start >= size {
		return 0, 0, failure(http.StatusRequestedRangeNotSatisfiable, "requestedRangeNotSatisfiable", "Request range not satisfiable")
	}
	return start, end, nil
}
