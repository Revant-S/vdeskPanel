//go:build devdrive

package main

import (
	"flag"
	"log/slog"
	"strings"

	"github.com/cosmoboy/vault/internal/config"
	"github.com/cosmoboy/vault/internal/daemon"
)

// With the devdrive tag, vaultd can talk to tools/drivefake instead of Google, for
// development and end-to-end tests. Release builds cannot skip OAuth.
type devDriveOptions struct {
	endpoint string
}

func registerDevDriveFlags(opts *devDriveOptions) {
	flag.StringVar(&opts.endpoint, "drive-endpoint", "", "base URL of a fake Drive, e.g. http://127.0.0.1:8080")
}

func driveConnector(opts devDriveOptions, cfg config.Config, logger *slog.Logger) (daemon.DriveConnector, error) {
	if opts.endpoint == "" {
		return googleConnector(cfg, logger), nil
	}
	base := strings.TrimSuffix(opts.endpoint, "/")
	return &daemon.EndpointConnector{Endpoint: base + "/drive/v3/", UploadEndpoint: base + "/upload/drive/v3/", Logger: logger}, nil
}
