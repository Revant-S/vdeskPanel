package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
)

// maxSocketPath is the size of sun_path minus its terminating NUL.
const maxSocketPath = 107

type Layout struct {
	ConfigFile      string
	IndexFile       string
	CacheDir        string
	TokenFile       string
	VaultHeaderFile string
	DeviceIDFile    string
	LogFile         string
	SocketPath      string
}

// HomeLayout keeps every file of an instance under one directory, so a second instance on
// the same machine is just another directory, and deleting it is a brand-new laptop.
func HomeLayout(home string) (Layout, error) {
	absolute, err := filepath.Abs(home)
	if err != nil {
		return Layout{}, err
	}
	return checkSocket(Layout{
		ConfigFile:      filepath.Join(absolute, "config.toml"),
		IndexFile:       filepath.Join(absolute, "index.db"),
		CacheDir:        filepath.Join(absolute, "cache"),
		TokenFile:       filepath.Join(absolute, "token.enc"),
		VaultHeaderFile: filepath.Join(absolute, "vault.header"),
		DeviceIDFile:    filepath.Join(absolute, "device-id"),
		LogFile:         filepath.Join(absolute, "daemon.log"),
		SocketPath:      filepath.Join(absolute, "daemon.sock"),
	})
}

// DefaultLayout spreads files over the XDG directories as spec section 6 lists them.
func DefaultLayout() (Layout, error) {
	data := filepath.Join(xdg.DataHome, "vault")
	return checkSocket(Layout{
		ConfigFile:      filepath.Join(xdg.ConfigHome, "vault", "config.toml"),
		IndexFile:       filepath.Join(data, "index.db"),
		CacheDir:        filepath.Join(data, "cache"),
		TokenFile:       filepath.Join(data, "token.enc"),
		VaultHeaderFile: filepath.Join(data, "vault.header"),
		DeviceIDFile:    filepath.Join(data, "device-id"),
		LogFile:         filepath.Join(xdg.StateHome, "vault", "daemon.log"),
		SocketPath:      filepath.Join(xdg.RuntimeDir, "vault", "daemon.sock"),
	})
}

func ResolveLayout(home string) (Layout, error) {
	if home != "" {
		return HomeLayout(home)
	}
	return DefaultLayout()
}

func (l *Layout) DataDirs() []string {
	return []string{filepath.Dir(l.ConfigFile), filepath.Dir(l.IndexFile), l.CacheDir, filepath.Dir(l.LogFile)}
}

// CreateDirs creates the instance's directories and makes them private to the owner, since
// they hold the index, the cache and the daemon socket.
func (l *Layout) CreateDirs() error {
	for _, dir := range l.DataDirs() {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("protect %s: %w", dir, err)
		}
	}
	return nil
}

func checkSocket(layout Layout) (Layout, error) {
	if len(layout.SocketPath) > maxSocketPath {
		return Layout{}, fmt.Errorf("socket path %s is longer than %d bytes; use a shorter --home", layout.SocketPath, maxSocketPath)
	}
	return layout, nil
}
