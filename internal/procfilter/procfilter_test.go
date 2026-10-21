package procfilter

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func writeFakeProcess(t *testing.T, root string, threadID, processID int, threadName, processName string) {
	t.Helper()
	for _, entry := range []struct {
		id   int
		name string
	}{{threadID, threadName}, {processID, processName}} {
		dir := filepath.Join(root, strconv.Itoa(entry.id))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "comm"), []byte(entry.name+"\n"), 0o644)
	}
	status := "Name:\t" + threadName + "\nTgid:\t" + strconv.Itoa(processID) + "\nPid:\t" + strconv.Itoa(threadID) + "\n"
	os.WriteFile(filepath.Join(root, strconv.Itoa(threadID), "status"), []byte(status), 0o644)
	os.WriteFile(filepath.Join(root, strconv.Itoa(processID), "status"), []byte("Tgid:\t"+strconv.Itoa(processID)+"\n"), 0o644)
}

func TestWorkerThreadOfDeniedProgramIsDenied(t *testing.T) {
	root := t.TempDir()
	writeFakeProcess(t, root, 4242, 4200, "pool-0", "tumblerd")
	writeFakeProcess(t, root, 5151, 5100, "gmain", "vlc")
	filter := New(&Proc{Root: root}, DefaultDenyList())
	if !filter.Denies(4242) {
		t.Fatal("tumblerd worker thread was allowed")
	}
	if filter.Denies(5151) {
		t.Fatal("vlc was denied")
	}
	if filter.Denies(9999) {
		t.Fatal("vanished thread was denied")
	}
}

func TestLongNamesMatchAfterKernelTruncation(t *testing.T) {
	root := t.TempDir()
	writeFakeProcess(t, root, 10, 10, "ffmpegthumbnail", "ffmpegthumbnail")
	if !New(&Proc{Root: root}, DefaultDenyList()).Denies(10) {
		t.Fatal("ffmpegthumbnailer not matched after truncation to 15 bytes")
	}
}

func TestRealProcessOfThisTest(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Skip("sleep unavailable")
	}
	defer cmd.Process.Kill()
	proc := SystemProc()
	pid := uint32(cmd.Process.Pid)
	if tgid, err := proc.ProcessOf(pid); err != nil || tgid != pid {
		t.Fatalf("ProcessOf(%d) = %d, %v", pid, tgid, err)
	}
	if !New(proc, []string{"sleep"}).Denies(pid) {
		t.Fatal("sleep not denied by a deny list naming it")
	}
}
