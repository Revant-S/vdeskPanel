// Package procfilter recognises thumbnailers and indexers so they cannot pull cloud-only
// files down just to make previews.
package procfilter

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// commLength is the kernel's limit on /proc/<pid>/comm, so deny-list names are compared
// after truncation to it.
const commLength = 15

func DefaultDenyList() []string {
	return []string{"gnome-thumbnail", "totem-video-thu", "ffmpegthumbnailer", "tumblerd", "localsearch-extr", "tracker-extract"}
}

// Proc reads process facts from a /proc tree; tests point it at a fake one.
type Proc struct {
	Root string
}

func SystemProc() Proc {
	return Proc{Root: "/proc"}
}

// ProcessOf maps the thread ID FUSE reports to its process ID. Worker threads of one
// program share a process ID but not a thread ID or thread name.
func (p Proc) ProcessOf(threadID uint32) (uint32, error) {
	file, err := os.Open(filepath.Join(p.Root, strconv.FormatUint(uint64(threadID), 10), "status"))
	if err != nil {
		return 0, fmt.Errorf("read thread status: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(scanner.Text(), "Tgid:"); ok {
			tgid, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
			return uint32(tgid), err
		}
	}
	return 0, fmt.Errorf("thread %d status has no Tgid", threadID)
}

func (p Proc) NameOf(processID uint32) (string, error) {
	data, err := os.ReadFile(filepath.Join(p.Root, strconv.FormatUint(uint64(processID), 10), "comm"))
	if err != nil {
		return "", fmt.Errorf("read process name: %w", err)
	}
	return string(bytes.TrimSpace(data)), nil
}

type Filter struct {
	proc   Proc
	denied map[string]bool
}

func New(proc Proc, denied []string) *Filter {
	names := make(map[string]bool, len(denied))
	for _, name := range denied {
		names[truncate(name)] = true
	}
	return &Filter{proc: proc, denied: names}
}

// Denies reports whether the thread belongs to a denied program. A thread that has already
// exited cannot be identified and is allowed, since refusing a real user would be worse.
func (f *Filter) Denies(threadID uint32) bool {
	processID, err := f.proc.ProcessOf(threadID)
	if err != nil {
		return false
	}
	name, err := f.proc.NameOf(processID)
	return err == nil && f.denied[truncate(name)]
}

func truncate(name string) string {
	if len(name) > commLength {
		return name[:commLength]
	}
	return name
}
