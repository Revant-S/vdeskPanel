package drive

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/cenkalti/backoff/v5"
)

// DownloadRange writes bytes [offset, offset+length) of id's content to w at the same
// offsets. A cut transfer resumes from the last byte written.
func (c *Client) DownloadRange(ctx context.Context, id string, offset, length int64, w io.WriterAt) error {
	if length <= 0 {
		return nil
	}
	var written int64
	_, err := callWithRetry(ctx, c, nil, "download range", func(ctx context.Context) (struct{}, error) {
		start := offset + written
		res, err := c.requestMedia(ctx, id, fmt.Sprintf("bytes=%d-%d", start, offset+length-1))
		if err != nil {
			return struct{}{}, err
		}
		defer drainAndClose(res.Body)
		if res.StatusCode != http.StatusPartialContent {
			return struct{}{}, backoff.Permanent(fmt.Errorf("drive: range request answered with status %d", res.StatusCode))
		}
		copied, err := io.Copy(io.NewOffsetWriter(w, start), io.LimitReader(res.Body, length-written))
		written += copied
		if err != nil {
			return struct{}{}, fmt.Errorf("read range body: %w", err)
		}
		if written < length {
			return struct{}{}, fmt.Errorf("read range body: %w", io.ErrUnexpectedEOF)
		}
		return struct{}{}, nil
	})
	return err
}

// DownloadAll streams id's whole content to w, resuming with a Range request after a cut.
func (c *Client) DownloadAll(ctx context.Context, id string, w io.Writer) error {
	var written int64
	_, err := callWithRetry(ctx, c, nil, "download file", func(ctx context.Context) (struct{}, error) {
		byteRange := ""
		if written > 0 {
			byteRange = fmt.Sprintf("bytes=%d-", written)
		}
		res, err := c.requestMedia(ctx, id, byteRange)
		if err != nil {
			return struct{}{}, err
		}
		defer drainAndClose(res.Body)
		copied, err := io.Copy(w, res.Body)
		written += copied
		if err != nil {
			return struct{}{}, fmt.Errorf("read file body: %w", err)
		}
		if res.ContentLength >= 0 && copied < res.ContentLength {
			return struct{}{}, fmt.Errorf("read file body: %w", io.ErrUnexpectedEOF)
		}
		return struct{}{}, nil
	})
	return err
}

func (c *Client) requestMedia(ctx context.Context, id, byteRange string) (*http.Response, error) {
	target := c.endpoint + "files/" + url.PathEscape(id) + "?alt=media"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, backoff.Permanent(err)
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if err := checkResponse(res); err != nil {
		drainAndClose(res.Body)
		return nil, err
	}
	return res, nil
}
