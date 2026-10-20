package secret

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Hardening is process-wide, so it runs in a child process to leave the test binary dumpable.
func TestHardenProcessDisablesDumpsInChild(t *testing.T) {
	if os.Getenv("VAULT_HARDEN_CHILD") == "1" {
		report, err := HardenProcess(false)
		if err != nil {
			t.Fatal(err)
		}
		dumpable, _ := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
		var core unix.Rlimit
		_ = unix.Getrlimit(unix.RLIMIT_CORE, &core)
		if dumpable != 0 || core.Cur != 0 || core.Max != 0 {
			t.Fatalf("dumpable=%d core=%+v", dumpable, core)
		}
		if report.KeysMaySwap() == memlockUnlimited() {
			t.Fatalf("report %+v inconsistent with memlock limit", report)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHardenProcessDisablesDumpsInChild$", "-test.v")
	cmd.Env = append(os.Environ(), "VAULT_HARDEN_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "PASS") {
		t.Fatalf("child failed: %v\n%s", err, output)
	}
}
