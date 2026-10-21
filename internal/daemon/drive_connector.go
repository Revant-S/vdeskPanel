package daemon

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/cosmoboy/vault/internal/auth"
	"github.com/cosmoboy/vault/internal/drive"
	"golang.org/x/oauth2"
)

type randomSource struct{}

func (randomSource) Read(p []byte) (int, error) { return rand.Read(p) }

// DriveConnector signs in to Google and builds Drive clients. Tests and the e2e harness
// swap in one that talks to the fake Drive.
type DriveConnector interface {
	BeginSignIn(ctx context.Context) (authURL string, wait func(context.Context) (*oauth2.Token, error), err error)
	Connect(ctx context.Context, tokens oauth2.TokenSource) (*drive.Client, error)
	TokenSource(ctx context.Context, token *oauth2.Token) oauth2.TokenSource
}

type GoogleConnector struct {
	OAuth  *oauth2.Config
	Logger *slog.Logger
}

func NewGoogleConnector(clientFile string, logger *slog.Logger) (*GoogleConnector, error) {
	credentials, err := auth.LoadClientCredentials(clientFile, auth.EmbeddedClientFile())
	if err != nil {
		return nil, err
	}
	return &GoogleConnector{OAuth: auth.NewConfig(credentials), Logger: logger}, nil
}

func (g *GoogleConnector) BeginSignIn(ctx context.Context) (string, func(context.Context) (*oauth2.Token, error), error) {
	return auth.NewFlow(g.OAuth, nil).Begin(ctx)
}

func (g *GoogleConnector) TokenSource(ctx context.Context, token *oauth2.Token) oauth2.TokenSource {
	return g.OAuth.TokenSource(ctx, token)
}

func (g *GoogleConnector) Connect(ctx context.Context, tokens oauth2.TokenSource) (*drive.Client, error) {
	httpClient := &http.Client{Transport: &oauth2.Transport{Source: tokens, Base: http.DefaultTransport}}
	return drive.NewClient(ctx, drive.DefaultConfig(), drive.Deps{HTTPClient: httpClient, Logger: g.Logger, Now: time.Now})
}

// EndpointConnector talks to a Drive-compatible endpoint without OAuth. It exists for the
// fake Drive used in development and end-to-end tests.
type EndpointConnector struct {
	Endpoint       string
	UploadEndpoint string
	HTTPClient     *http.Client
	Config         drive.Config
	Logger         *slog.Logger
}

func (e *EndpointConnector) BeginSignIn(context.Context) (string, func(context.Context) (*oauth2.Token, error), error) {
	token := &oauth2.Token{AccessToken: "dev", RefreshToken: "dev", Expiry: time.Now().Add(24 * time.Hour)}
	return "about:blank", func(context.Context) (*oauth2.Token, error) { return token, nil }, nil
}

func (e *EndpointConnector) TokenSource(_ context.Context, token *oauth2.Token) oauth2.TokenSource {
	return oauth2.StaticTokenSource(token)
}

func (e *EndpointConnector) Connect(ctx context.Context, _ oauth2.TokenSource) (*drive.Client, error) {
	config := e.Config
	if config.ChunkSize == 0 {
		config = drive.DefaultConfig()
	}
	config.Endpoint, config.UploadEndpoint = e.Endpoint, e.UploadEndpoint
	client := e.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	c, err := drive.NewClient(ctx, config, drive.Deps{HTTPClient: client, Logger: e.Logger, Now: time.Now})
	if err != nil {
		return nil, fmt.Errorf("connect to drive endpoint: %w", err)
	}
	return c, nil
}
