package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cenkalti/backoff/v5"
	drive "google.golang.org/api/drive/v3"

	"github.com/cosmoboy/vault/internal/model"
)

// UploadSession is persisted by the caller so an upload can resume after a restart.
type UploadSession struct {
	URI  string
	Size int64
}

func (c *Client) StartResumableUpload(ctx context.Context, file NewFile, size int64) (UploadSession, error) {
	if size <= 0 {
		return UploadSession{}, fmt.Errorf("drive: resumable upload needs a positive size, got %d", size)
	}
	metadata, err := json.Marshal(file.toDrive(contentMimeType))
	if err != nil {
		return UploadSession{}, fmt.Errorf("encode upload metadata: %w", err)
	}
	target := c.uploadEndpoint + "files?" + url.Values{"uploadType": {"resumable"}, "fields": {fileFields}}.Encode()
	uri, err := callWithRetry(ctx, c, c.budget.metadataWrites, "start upload", func(ctx context.Context) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(metadata))
		if err != nil {
			return "", backoff.Permanent(err)
		}
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
		req.Header.Set("X-Upload-Content-Type", contentMimeType)
		req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
		res, err := c.httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer drainAndClose(res.Body)
		if err := checkResponse(res); err != nil {
			return "", err
		}
		location := res.Header.Get("Location")
		if location == "" {
			return "", errors.New("drive: upload session has no location")
		}
		return location, nil
	})
	if err != nil {
		return UploadSession{}, err
	}
	return UploadSession{URI: uri, Size: size}, nil
}

type uploadProgress struct {
	offset        int64
	statusUnknown bool
	onConfirmed   func(offset int64)
}

func (p *uploadProgress) confirm(offset int64) {
	p.statusUnknown = false
	if offset == p.offset {
		return
	}
	p.offset = offset
	if p.onConfirmed != nil {
		p.onConfirmed(offset)
	}
}

// ResumeUpload asks Drive how many bytes of the session it holds, then sends the rest in
// chunks. After any failure it re-asks, so no confirmed byte is sent twice.
func (c *Client) ResumeUpload(ctx context.Context, session UploadSession, content io.ReaderAt, onConfirmed func(offset int64)) (model.RemoteFile, error) {
	progress := &uploadProgress{statusUnknown: true, onConfirmed: onConfirmed}
	for {
		file, err := callWithRetry(ctx, c, nil, "upload chunk", func(ctx context.Context) (*drive.File, error) {
			return c.advanceUpload(ctx, session, content, progress)
		})
		if errors.Is(err, ErrNotFound) || statusOf(err) == http.StatusGone {
			return model.RemoteFile{}, fmt.Errorf("%w: %w", ErrUploadSessionExpired, err)
		}
		if err != nil {
			return model.RemoteFile{}, err
		}
		if file != nil {
			return toRemoteFile(file), nil
		}
	}
}

func (c *Client) advanceUpload(ctx context.Context, session UploadSession, content io.ReaderAt, progress *uploadProgress) (*drive.File, error) {
	if progress.statusUnknown {
		file, offset, err := c.exchangeUploadRange(ctx, session, nil, fmt.Sprintf("bytes */%d", session.Size), 0)
		if err != nil || file != nil {
			return file, err
		}
		progress.confirm(offset)
	}
	start := progress.offset
	end := min(start+c.chunkSize, session.Size)
	chunk := io.NewSectionReader(content, start, end-start)
	contentRange := fmt.Sprintf("bytes %d-%d/%d", start, end-1, session.Size)
	file, offset, err := c.exchangeUploadRange(ctx, session, chunk, contentRange, end-start)
	if err != nil {
		progress.statusUnknown = true
		return nil, err
	}
	if file != nil {
		return file, nil
	}
	progress.confirm(offset)
	if offset <= start {
		return nil, errors.New("drive: upload chunk was not persisted")
	}
	return nil, nil
}

// exchangeUploadRange sends one PUT to the session. Drive answers 308 with the persisted
// range while incomplete, or 200/201 with the file once the last byte arrives.
func (c *Client) exchangeUploadRange(ctx context.Context, session UploadSession, body io.Reader, contentRange string, length int64) (*drive.File, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, session.URI, body)
	if err != nil {
		return nil, 0, backoff.Permanent(err)
	}
	req.ContentLength = length
	req.Header.Set("Content-Range", contentRange)
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer drainAndClose(res.Body)
	if res.StatusCode == http.StatusPermanentRedirect {
		offset, err := parsePersistedRange(res.Header.Get("Range"))
		return nil, offset, err
	}
	if err := checkResponse(res); err != nil {
		return nil, 0, err
	}
	var file drive.File
	if err := json.NewDecoder(res.Body).Decode(&file); err != nil {
		return nil, 0, fmt.Errorf("decode uploaded file: %w", err)
	}
	return &file, session.Size, nil
}

// parsePersistedRange reads Drive's "bytes=0-N" header; no header means nothing persisted.
func parsePersistedRange(header string) (int64, error) {
	if header == "" {
		return 0, nil
	}
	_, last, found := strings.Cut(strings.TrimPrefix(header, "bytes="), "-")
	if !found {
		return 0, fmt.Errorf("drive: malformed upload range %q", header)
	}
	lastByte, err := strconv.ParseInt(last, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("drive: malformed upload range %q: %w", header, err)
	}
	return lastByte + 1, nil
}
