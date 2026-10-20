package drive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	drive "google.golang.org/api/drive/v3"
	"google.golang.org/api/option"

	"github.com/cosmoboy/vault/internal/model"
)

const (
	DefaultEndpoint       = "https://www.googleapis.com/drive/v3/"
	DefaultUploadEndpoint = "https://www.googleapis.com/upload/drive/v3/"

	FolderMimeType  = "application/vnd.google-apps.folder"
	contentMimeType = "application/octet-stream"

	MaxSmallFileSize = 5 << 20
	// Drive rejects resumable chunks that are not a multiple of 256 KiB, except the last.
	uploadChunkAlignment = 256 << 10

	fileFields = "id,name,parents,appProperties,md5Checksum,size,createdTime,modifiedTime,trashed,mimeType"
)

type Config struct {
	Endpoint       string
	UploadEndpoint string
	ChunkSize      int64
	Budget         BudgetConfig
	Retry          RetryConfig
}

func DefaultConfig() Config {
	return Config{
		Endpoint:       DefaultEndpoint,
		UploadEndpoint: DefaultUploadEndpoint,
		ChunkSize:      16 << 20,
		Budget:         DefaultBudgetConfig(),
		Retry:          DefaultRetryConfig(),
	}
}

type Deps struct {
	HTTPClient     *http.Client
	TokenRefresher TokenRefresher
	Logger         *slog.Logger
	Now            func() time.Time
}

type Client struct {
	service        *drive.Service
	httpClient     *http.Client
	endpoint       string
	uploadEndpoint string
	chunkSize      int64
	retry          RetryConfig
	budget         *Budget
	refresher      TokenRefresher
	logger         *slog.Logger
}

func NewClient(ctx context.Context, cfg Config, deps Deps) (*Client, error) {
	if deps.HTTPClient == nil {
		return nil, errors.New("drive: http client is required")
	}
	if cfg.ChunkSize <= 0 || cfg.ChunkSize%uploadChunkAlignment != 0 {
		return nil, fmt.Errorf("drive: chunk size %d is not a positive multiple of %d", cfg.ChunkSize, uploadChunkAlignment)
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	service, err := drive.NewService(ctx,
		option.WithEndpoint(withTrailingSlash(cfg.Endpoint)),
		option.WithHTTPClient(deps.HTTPClient),
		option.WithoutAuthentication(),
		// The library's own debug logger would dump request bodies, which carry names.
		option.WithLogger(slog.New(slog.DiscardHandler)),
	)
	if err != nil {
		return nil, fmt.Errorf("create drive service: %w", err)
	}
	return &Client{
		service:        service,
		httpClient:     deps.HTTPClient,
		endpoint:       withTrailingSlash(cfg.Endpoint),
		uploadEndpoint: withTrailingSlash(cfg.UploadEndpoint),
		chunkSize:      cfg.ChunkSize,
		retry:          cfg.Retry,
		budget:         newBudget(cfg.Budget, now),
		refresher:      deps.TokenRefresher,
		logger:         logger,
	}, nil
}

func (c *Client) Budget() *Budget { return c.budget }

// NewFile describes an object to create. ID is a pre-generated Drive ID from GenerateIDs;
// empty lets Drive choose one.
type NewFile struct {
	ID           string
	Name         string
	ParentID     string
	ModifiedTime time.Time
	Properties   map[string]string
}

func (f NewFile) toDrive(mimeType string) *drive.File {
	file := &drive.File{
		Id:            f.ID,
		Name:          f.Name,
		MimeType:      mimeType,
		AppProperties: f.Properties,
	}
	if f.ParentID != "" {
		file.Parents = []string{f.ParentID}
	}
	if !f.ModifiedTime.IsZero() {
		file.ModifiedTime = formatTime(f.ModifiedTime)
	}
	return file
}

func toRemoteFile(file *drive.File) model.RemoteFile {
	return model.RemoteFile{
		ID:           file.Id,
		Name:         file.Name,
		MD5:          file.Md5Checksum,
		Parents:      file.Parents,
		Size:         file.Size,
		ModifiedTime: parseTime(file.ModifiedTime),
		CreatedTime:  parseTime(file.CreatedTime),
		Folder:       file.MimeType == FolderMimeType,
		Trashed:      file.Trashed,
		Properties:   file.AppProperties,
	}
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func withTrailingSlash(endpoint string) string {
	if strings.HasSuffix(endpoint, "/") {
		return endpoint
	}
	return endpoint + "/"
}
