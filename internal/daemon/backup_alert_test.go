package daemon

import (
	"testing"
	"time"
)

func TestBackupAlertFiresOncePerStretchOnline(t *testing.T) {
	start := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	at := func(minutes int) time.Time { return start.Add(time.Duration(minutes) * time.Minute) }
	alert := &backupAlert{delay: 10 * time.Minute}
	steps := []struct {
		minute  int
		backlog int
		online  bool
		want    bool
	}{
		{0, 3, true, false},
		{9, 3, true, false},
		{10, 3, true, true},
		{11, 5, true, false},
		{12, 0, true, false},
		{13, 1, true, false},
		{20, 1, false, false},
		{21, 1, true, false},
		{30, 1, true, false},
		{31, 1, true, true},
	}
	for _, step := range steps {
		if got := alert.observe(at(step.minute), step.backlog, step.online); got != step.want {
			t.Fatalf("minute %d: alert = %v, want %v", step.minute, got, step.want)
		}
	}
}
