package auth

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

var (
	ErrPlaceholderCredentials = errors.New("auth: OAuth client is still the committed placeholder; set oauth_client_file to a Google desktop client JSON")
	ErrInvalidCredentials     = errors.New("auth: invalid OAuth client file")
)

const placeholderMarker = "replace-me"

//go:embed oauth_client.json
var embeddedClientFile []byte

type ClientCredentials struct {
	ClientID     string
	ClientSecret string
}

// EmbeddedClientFile returns a copy so callers cannot alter the compiled-in bytes.
func EmbeddedClientFile() []byte {
	return bytes.Clone(embeddedClientFile)
}

// LoadClientCredentials prefers the file at overridePath when it exists, else embedded.
func LoadClientCredentials(overridePath string, embedded []byte) (ClientCredentials, error) {
	data, source, err := readClientFile(overridePath, embedded)
	if err != nil {
		return ClientCredentials{}, err
	}
	credentials, err := parseClientFile(data)
	if err != nil {
		return ClientCredentials{}, fmt.Errorf("%s: %w", source, err)
	}
	if isPlaceholder(credentials) {
		return ClientCredentials{}, ErrPlaceholderCredentials
	}
	return credentials, nil
}

func readClientFile(overridePath string, embedded []byte) ([]byte, string, error) {
	if overridePath != "" {
		data, err := os.ReadFile(overridePath)
		if err == nil {
			return data, overridePath, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, "", fmt.Errorf("read OAuth client file: %w", err)
		}
	}
	return embedded, "embedded OAuth client", nil
}

// parseClientFile reads Google's "installed" (desktop app) client format. A "web" client
// is rejected because Google does not allow it an arbitrary loopback port.
func parseClientFile(data []byte) (ClientCredentials, error) {
	var file struct {
		Installed *struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"installed"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return ClientCredentials{}, fmt.Errorf("%w: %w", ErrInvalidCredentials, err)
	}
	if file.Installed == nil {
		return ClientCredentials{}, fmt.Errorf("%w: no \"installed\" desktop client", ErrInvalidCredentials)
	}
	if file.Installed.ClientID == "" || file.Installed.ClientSecret == "" {
		return ClientCredentials{}, fmt.Errorf("%w: client_id or client_secret missing", ErrInvalidCredentials)
	}
	return ClientCredentials{ClientID: file.Installed.ClientID, ClientSecret: file.Installed.ClientSecret}, nil
}

func isPlaceholder(credentials ClientCredentials) bool {
	return strings.HasPrefix(credentials.ClientID, placeholderMarker) ||
		credentials.ClientSecret == placeholderMarker
}
