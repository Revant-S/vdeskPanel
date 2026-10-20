//go:build devdrive

// Command drivefake serves the in-memory fake Drive on a localhost port for manual and e2e
// runs. State lives only in memory and is gone when the process exits.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cosmoboy/vault/internal/drive/drivefake"
)

func main() {
	address := flag.String("listen", "127.0.0.1:0", "address to listen on; keep it on loopback")
	flag.Parse()
	if err := serve(*address); err != nil {
		slog.Error("drivefake stopped", "err", err)
		os.Exit(1)
	}
}

func serve(address string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	server := &http.Server{Handler: drivefake.New().Handler(), ReadHeaderTimeout: 10 * time.Second}
	base := "http://" + listener.Addr().String()
	fmt.Printf("endpoint=%s/drive/v3/\nupload_endpoint=%s/upload/drive/v3/\n", base, base)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down: %w", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
