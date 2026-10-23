package drive

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	drive "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"

	"vault/internal/model"
)

const listPageSize = 1000

func (c *Client) ListChildren(ctx context.Context, parentID string) ([]model.RemoteFile, error) {
	return c.listAll(ctx, queryFor(parentID), "list children")
}

// FindByProperties lists objects whose appProperties contain every given pair. An empty
// parentID searches every object the app can see.
func (c *Client) FindByProperties(ctx context.Context, parentID string, properties map[string]string) ([]model.RemoteFile, error) {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	clauses := make([]string, 0, len(keys))
	for _, key := range keys {
		clauses = append(clauses, fmt.Sprintf("appProperties has { key=%s and value=%s }", quote(key), quote(properties[key])))
	}
	return c.listAll(ctx, queryFor(parentID, clauses...), "find by properties")
}

func (c *Client) listAll(ctx context.Context, query, operation string) ([]model.RemoteFile, error) {
	var files []model.RemoteFile
	pageToken := ""
	for {
		page, err := callWithRetry(ctx, c, c.budget.reads, operation, func(ctx context.Context) (*drive.FileList, error) {
			return c.service.Files.List().Q(query).PageSize(listPageSize).PageToken(pageToken).
				Fields(googleapi.Field("nextPageToken,files(" + fileFields + ")")).Context(ctx).Do()
		})
		if err != nil {
			return nil, err
		}
		for _, file := range page.Files {
			files = append(files, toRemoteFile(file))
		}
		if page.NextPageToken == "" {
			return files, nil
		}
		pageToken = page.NextPageToken
	}
}

func (c *Client) StartPageToken(ctx context.Context) (string, error) {
	token, err := callWithRetry(ctx, c, c.budget.reads, "get start page token", func(ctx context.Context) (*drive.StartPageToken, error) {
		return c.service.Changes.GetStartPageToken().Context(ctx).Do()
	})
	if err != nil {
		return "", err
	}
	return token.StartPageToken, nil
}

// Changes returns one page of changes after pageToken. Callers follow NextPageToken until
// NewStartPageToken is set and store that for the next poll.
func (c *Client) Changes(ctx context.Context, pageToken string) (model.ChangesPage, error) {
	fields := googleapi.Field("nextPageToken,newStartPageToken,changes(fileId,removed,file(" + fileFields + "))")
	list, err := callWithRetry(ctx, c, c.budget.reads, "list changes", func(ctx context.Context) (*drive.ChangeList, error) {
		return c.service.Changes.List(pageToken).PageSize(listPageSize).IncludeRemoved(true).
			Spaces("drive").Fields(fields).Context(ctx).Do()
	})
	if isExpiredPageToken(err) {
		return model.ChangesPage{}, fmt.Errorf("%w: %w", ErrPageTokenExpired, err)
	}
	if err != nil {
		return model.ChangesPage{}, err
	}
	page := model.ChangesPage{NextPageToken: list.NextPageToken, NewStartPageToken: list.NewStartPageToken}
	for _, change := range list.Changes {
		remote := model.RemoteChange{FileID: change.FileId, Removed: change.Removed}
		if change.File != nil {
			file := toRemoteFile(change.File)
			remote.File = &file
		}
		page.Changes = append(page.Changes, remote)
	}
	return page, nil
}

// Drive answers an expired or unknown page token with either 404 or 400 "invalid".
func isExpiredPageToken(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Status == http.StatusNotFound ||
		(apiErr.Status == http.StatusBadRequest && apiErr.Reason == reasonInvalid)
}

func queryFor(parentID string, clauses ...string) string {
	all := make([]string, 0, len(clauses)+2)
	if parentID != "" {
		all = append(all, quote(parentID)+" in parents")
	}
	all = append(all, clauses...)
	all = append(all, "trashed = false")
	return strings.Join(all, " and ")
}

func nameClause(name string) string { return "name = " + quote(name) }

func mimeTypeClause(mimeType string) string { return "mimeType = " + quote(mimeType) }

func quote(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value)
	return "'" + escaped + "'"
}

func joinIDs(ids []string) string { return strings.Join(ids, ",") }
