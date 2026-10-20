// Package plaintextcheck lets tests assert that known secrets never reach disk or logs.
package plaintextcheck

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func AssertBytesClean(t testing.TB, what string, data []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(data, []byte(secret)) {
			t.Errorf("%s contains plaintext %q", what, secret)
		}
	}
}

func AssertDirClean(t testing.TB, root string, secrets ...string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		AssertBytesClean(t, "path "+relative, []byte(relative), secrets...)
		if !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		AssertBytesClean(t, "file "+relative, data, secrets...)
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
}
