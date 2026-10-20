package plaintextcheck

import (
	"os"
	"path/filepath"
	"testing"
)

type recordingTB struct {
	testing.TB
	failures int
}

func (r *recordingTB) Errorf(string, ...any) { r.failures++ }
func (r *recordingTB) Helper()               {}

func TestAssertDirCleanFindsNamesAndContents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.pdf"), []byte("quarterly numbers"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingTB{TB: t}
	AssertDirClean(recorder, root, "report.pdf", "quarterly")
	if recorder.failures != 2 {
		t.Fatalf("failures = %d, want 2", recorder.failures)
	}
}

func TestAssertDirCleanPassesOnCiphertext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Xk3p9Qa"), []byte{0x01, 0x02}, 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingTB{TB: t}
	AssertDirClean(recorder, root, "report.pdf")
	if recorder.failures != 0 {
		t.Fatalf("failures = %d, want 0", recorder.failures)
	}
}
