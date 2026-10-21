package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client talks to the daemon over its Unix socket. The panel and the CLI share it.
type Client struct {
	http *http.Client
}

var _ Service = (*Client)(nil)

func NewClient(socketPath string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socketPath)
	}}
	return &Client{http: &http.Client{Transport: transport}}
}

func (c *Client) call(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string, result any) error {
	target := "http://vault" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("reach the vault daemon: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return decodeError(response)
	}
	if result == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(result)
}

func (c *Client) callJSON(ctx context.Context, method, path string, query url.Values, body, result any) error {
	if body == nil {
		return c.call(ctx, method, path, query, nil, "", result)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.call(ctx, method, path, query, bytes.NewReader(encoded), "application/json", result)
}

func (c *Client) callWithPassword(ctx context.Context, path string, query url.Values, password []byte) error {
	return c.call(ctx, http.MethodPost, path, query, bytes.NewReader(password), "application/octet-stream", nil)
}

func decodeError(response *http.Response) error {
	var body ErrorBody
	json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body)
	failure := &ResponseError{StatusCode: response.StatusCode, Code: body.Code, Message: body.Error}
	if sentinel := sentinelForCode(body.Code); sentinel != nil {
		return errors.Join(sentinel, failure)
	}
	return failure
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	return status, c.callJSON(ctx, http.MethodGet, "/status", nil, nil, &status)
}

func (c *Client) BeginSignIn(ctx context.Context) (string, error) {
	var body authURLBody
	return body.AuthURL, c.callJSON(ctx, http.MethodPost, "/signin", nil, nil, &body)
}

func (c *Client) SignOut(ctx context.Context) error {
	return c.callJSON(ctx, http.MethodPost, "/signout", nil, nil, nil)
}

func (c *Client) CreateVault(ctx context.Context, password []byte, deviceName string) error {
	return c.callWithPassword(ctx, "/create", url.Values{"deviceName": {deviceName}}, password)
}

func (c *Client) JoinVault(ctx context.Context, password []byte, deviceName string, mode JoinMode) error {
	return c.callWithPassword(ctx, "/join", url.Values{"deviceName": {deviceName}, "mode": {string(mode)}}, password)
}

func (c *Client) Unlock(ctx context.Context, password []byte) error {
	return c.callWithPassword(ctx, "/unlock", nil, password)
}

func (c *Client) Lock(ctx context.Context) error {
	return c.callJSON(ctx, http.MethodPost, "/lock", nil, nil, nil)
}

func (c *Client) Tree(ctx context.Context, path string) ([]TreeEntry, error) {
	var entries []TreeEntry
	return entries, c.callJSON(ctx, http.MethodGet, "/tree", url.Values{"path": {path}}, nil, &entries)
}

func (c *Client) Commits(ctx context.Context, before string, limit int, path string) (CommitPage, error) {
	var page CommitPage
	query := url.Values{"before": {before}, "limit": {strconv.Itoa(limit)}, "path": {path}}
	return page, c.callJSON(ctx, http.MethodGet, "/commits", query, nil, &page)
}

func (c *Client) Devices(ctx context.Context) ([]DeviceView, error) {
	var devices []DeviceView
	return devices, c.callJSON(ctx, http.MethodGet, "/devices", nil, nil, &devices)
}

func (c *Client) RetireDevice(ctx context.Context, deviceID string) error {
	return c.callJSON(ctx, http.MethodPost, "/devices/retire", nil, idBody{ID: deviceID}, nil)
}

func (c *Client) Trash(ctx context.Context) ([]TrashItem, error) {
	var items []TrashItem
	return items, c.callJSON(ctx, http.MethodGet, "/trash", nil, nil, &items)
}

func (c *Client) RestoreTrash(ctx context.Context, id string) (string, error) {
	var body pathBody
	return body.Path, c.callJSON(ctx, http.MethodPost, "/trash/restore", nil, idBody{ID: id}, &body)
}

func (c *Client) PurgeTrash(ctx context.Context, id string) error {
	return c.callJSON(ctx, http.MethodPost, "/trash/purge", nil, idBody{ID: id}, nil)
}

func (c *Client) EmptyTrash(ctx context.Context) (EmptyTrashResult, error) {
	var result EmptyTrashResult
	return result, c.callJSON(ctx, http.MethodPost, "/trash/empty", nil, nil, &result)
}

func (c *Client) Download(ctx context.Context, path string) error {
	return c.callJSON(ctx, http.MethodPost, "/download", nil, pathBody{Path: path}, nil)
}

func (c *Client) Free(ctx context.Context, path string) error {
	return c.callJSON(ctx, http.MethodPost, "/free", nil, pathBody{Path: path}, nil)
}

func (c *Client) SyncNow(ctx context.Context) error {
	return c.callJSON(ctx, http.MethodPost, "/sync", nil, nil, nil)
}

func (c *Client) Verify(ctx context.Context, deep bool) (VerifyReport, error) {
	var report VerifyReport
	return report, c.callJSON(ctx, http.MethodPost, "/verify", url.Values{"deep": {strconv.FormatBool(deep)}}, nil, &report)
}

func (c *Client) PauseRestore(ctx context.Context, paused bool) error {
	return c.callJSON(ctx, http.MethodPost, "/restore/pause", url.Values{"paused": {strconv.FormatBool(paused)}}, nil, nil)
}

func (c *Client) Settings(ctx context.Context) (Settings, error) {
	var settings Settings
	return settings, c.callJSON(ctx, http.MethodGet, "/settings", nil, nil, &settings)
}

func (c *Client) UpdateSettings(ctx context.Context, settings Settings) error {
	return c.callJSON(ctx, http.MethodPut, "/settings", nil, settings, nil)
}

// Subscribe streams events, reconnecting after a dropped connection until ctx ends.
func (c *Client) Subscribe(ctx context.Context) <-chan Event {
	events := make(chan Event, 16)
	go func() {
		defer close(events)
		for ctx.Err() == nil {
			c.streamEvents(ctx, events)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()
	return events
}
