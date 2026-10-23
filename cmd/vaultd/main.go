// Command vaultd is the vault daemon: it serves the mount, holds the keys and syncs with Drive.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"vault/internal/api"
	"vault/internal/config"
	"vault/internal/daemon"
	"vault/internal/notify"
	"vault/internal/secret"
)

const stopTimeout = 50 * time.Second

type options struct {
	home     string
	logLevel string
	devDrive devDriveOptions
}

func main() {
	var opts options
	flag.StringVar(&opts.home, "home", "", "keep every file of this instance under this directory")
	flag.StringVar(&opts.logLevel, "log-level", "info", "debug, info, warn or error")
	registerDevDriveFlags(&opts.devDrive)
	flag.Parse()
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "vaultd:", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	layout, err := config.ResolveLayout(opts.home)
	if err != nil {
		return err
	}
	cfg, err := config.Load(layout.ConfigFile, config.Defaults(defaultsBase(opts.home)))
	if err != nil {
		return err
	}
	hardening, err := secret.HardenProcess(cfg.LockAllMemory)
	if err != nil {
		return err
	}
	if err := layout.CreateDirs(); err != nil {
		return err
	}
	logger, closeLog, err := openLog(layout.LogFile, opts.logLevel)
	if err != nil {
		return err
	}
	defer closeLog()
	connector, err := driveConnector(opts.devDrive, cfg, logger)
	if err != nil {
		return err
	}
	deps := daemon.Deps{Layout: layout, Drive: connector, Hardening: hardening, Now: time.Now, Logger: logger}
	if desktop, err := notify.ConnectSessionBus(); err == nil {
		deps.Notifier = desktop
	} else {
		logger.Warn("desktop notifications unavailable", "err", err)
	}
	d, err := daemon.New(deps, cfg)
	if err != nil {
		return err
	}
	return serve(d, layout.SocketPath, logger)
}

func serve(d *daemon.Daemon, socketPath string, logger *slog.Logger) error {
	listener, err := api.Listen(socketPath)
	if err != nil {
		return err
	}
	server := api.NewServer(d, logger)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	logger.Info("vaultd ready", "socket", socketPath)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-signals:
	case err := <-served:
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	lockErr := d.Lock(ctx)
	return errors.Join(lockErr, server.Shutdown(ctx))
}

// defaultsBase decides where the default mount point goes: ~/Vault for the main instance,
// <home>/Vault for an instance started with --home, so two instances never share one.
func defaultsBase(home string) string {
	if home != "" {
		absolute, _ := filepath.Abs(home)
		return absolute
	}
	userHome, _ := os.UserHomeDir()
	return userHome
}

func openLog(path, level string) (*slog.Logger, func(), error) {
	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(level)); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	logger := slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{Level: logLevel}))
	return logger, func() { file.Close() }, nil
}

func googleConnector(cfg config.Config, logger *slog.Logger) daemon.DriveConnector {
	clientFile := cfg.OAuthClientFile
	if clientFile == "" {
		configDir, _ := os.UserConfigDir()
		clientFile = filepath.Join(configDir, "vault", "oauth_client.json")
	}
	connector, err := daemon.NewGoogleConnector(clientFile, logger)
	if err != nil {
		logger.Error("Google sign-in is not configured", "err", err)
		return &daemon.UnavailableConnector{Reason: err}
	}
	return connector
}
