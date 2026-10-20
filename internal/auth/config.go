// Package auth signs in to Google with OAuth 2.0 (PKCE, loopback redirect) and keeps the
// resulting token sealed under the vault's token subkey.
package auth

import (
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// DriveFileScope limits the app to files it created itself (spec §4).
const DriveFileScope = "https://www.googleapis.com/auth/drive.file"

// NewConfig uses the dependency-free endpoints package rather than oauth2/google, which
// would pull in cloud.google.com/go/compute/metadata for service-account support we never use.
func NewConfig(credentials ClientCredentials) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     credentials.ClientID,
		ClientSecret: credentials.ClientSecret,
		Endpoint:     endpoints.Google,
		Scopes:       []string{DriveFileScope},
	}
}
