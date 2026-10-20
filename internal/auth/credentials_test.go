package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const realClientFile = `{"installed":{"client_id":"123-abc.apps.googleusercontent.com","client_secret":"GOCSPX-test","redirect_uris":["http://localhost"]}}`

func writeClientFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oauth_client.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOverrideFileWinsOverEmbedded(t *testing.T) {
	path := writeClientFile(t, realClientFile)
	credentials, err := LoadClientCredentials(path, EmbeddedClientFile())
	if err != nil {
		t.Fatal(err)
	}
	want := ClientCredentials{ClientID: "123-abc.apps.googleusercontent.com", ClientSecret: "GOCSPX-test"}
	if credentials != want {
		t.Fatalf("credentials = %+v, want %+v", credentials, want)
	}
}

func TestEmbeddedUsedWhenOverrideMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.json")
	credentials, err := LoadClientCredentials(missing, []byte(realClientFile))
	if err != nil || credentials.ClientID != "123-abc.apps.googleusercontent.com" {
		t.Fatalf("LoadClientCredentials = %+v, %v", credentials, err)
	}
}

func TestPlaceholderOnlyIsRejected(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.json")
	for _, override := range []string{"", missing} {
		_, err := LoadClientCredentials(override, EmbeddedClientFile())
		if !errors.Is(err, ErrPlaceholderCredentials) {
			t.Fatalf("override %q: err = %v, want ErrPlaceholderCredentials", override, err)
		}
	}
}

func TestMalformedClientFilesFail(t *testing.T) {
	cases := map[string]string{
		"not json":       `{"installed":`,
		"web client":     `{"web":{"client_id":"a","client_secret":"b"}}`,
		"missing secret": `{"installed":{"client_id":"a"}}`,
	}
	for name, content := range cases {
		_, err := LoadClientCredentials(writeClientFile(t, content), EmbeddedClientFile())
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: err = %v, want ErrInvalidCredentials", name, err)
		}
	}
}

func TestUnreadableOverrideIsAnError(t *testing.T) {
	_, err := LoadClientCredentials(t.TempDir(), []byte(realClientFile))
	if err == nil {
		t.Fatal("reading a directory as the client file succeeded")
	}
}

func TestNewConfigRequestsOnlyDriveFileScope(t *testing.T) {
	config := NewConfig(ClientCredentials{ClientID: "id", ClientSecret: "secret"})
	if len(config.Scopes) != 1 || config.Scopes[0] != DriveFileScope {
		t.Fatalf("scopes = %v", config.Scopes)
	}
	if config.Endpoint.TokenURL != "https://oauth2.googleapis.com/token" {
		t.Fatalf("token endpoint = %q", config.Endpoint.TokenURL)
	}
}
