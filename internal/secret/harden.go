package secret

import (
	"fmt"

	"golang.org/x/sys/unix"
)

type HardeningReport struct {
	AllMemoryLocked bool
}

func (r HardeningReport) KeysMaySwap() bool {
	return !r.AllMemoryLocked
}

// HardenProcess stops core dumps and ptrace-style reads. It locks all memory only when that
// cannot exhaust RLIMIT_MEMLOCK, since a failed mmap under MCL_FUTURE kills the Go runtime.
func HardenProcess(lockAllMemory bool) (HardeningReport, error) {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return HardeningReport{}, fmt.Errorf("disable dumpable: %w", err)
	}
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}); err != nil {
		return HardeningReport{}, fmt.Errorf("disable core dumps: %w", err)
	}
	if !lockAllMemory && !memlockUnlimited() {
		return HardeningReport{}, nil
	}
	if err := unix.Mlockall(unix.MCL_CURRENT | unix.MCL_FUTURE); err != nil {
		return HardeningReport{}, fmt.Errorf("lock all memory: %w", err)
	}
	return HardeningReport{AllMemoryLocked: true}, nil
}

func memlockUnlimited() bool {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &limit); err != nil {
		return false
	}
	return limit.Cur == unix.RLIM_INFINITY
}
