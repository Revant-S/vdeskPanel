//go:build !devdrive

package main

import (
	"log/slog"

	"vault/internal/config"
	"vault/internal/daemon"
)

type devDriveOptions struct{}

func registerDevDriveFlags(*devDriveOptions) {}

func driveConnector(_ devDriveOptions, cfg config.Config, logger *slog.Logger) (daemon.DriveConnector, error) {
	return googleConnector(cfg, logger), nil
}
