package daemon

import (
	"context"
	"time"

	"github.com/cosmoboy/vault/internal/api"
	"github.com/cosmoboy/vault/internal/notify"
)

const (
	backupAlertDelay = 10 * time.Minute
	backupCheckEvery = time.Minute
)

// backupAlert fires once per stretch in which changes stay not backed up while online. Time
// offline does not count, and the stretch ends when everything is backed up.
type backupAlert struct {
	delay   time.Duration
	since   time.Time
	alerted bool
}

func (a *backupAlert) observe(now time.Time, backlog int, online bool) bool {
	if backlog == 0 {
		a.since, a.alerted = time.Time{}, false
		return false
	}
	if !online {
		a.since = time.Time{}
		return false
	}
	if a.since.IsZero() {
		a.since = now
	}
	if a.alerted || now.Sub(a.since) < a.delay {
		return false
	}
	a.alerted = true
	return true
}

// online is true while Drive keeps answering the change polls: two missed idle polls plus a
// minute of slack means offline.
func (s *session) online() bool {
	offlineAfter := 2*s.daemon.config.Polling.Idle.Duration + time.Minute
	return s.daemon.Now().Sub(s.client.LastContact()) < offlineAfter
}

func (s *session) watchBackup(ctx context.Context) error {
	alert := &backupAlert{delay: backupAlertDelay}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backupCheckEvery):
		}
		files, bytes, err := s.outgoing.Backlog(ctx)
		if err != nil || !alert.observe(s.daemon.Now(), files, s.online()) || s.daemon.Notifier == nil {
			continue
		}
		summary, body := notify.NotBackedUp(files, api.FormatBytes(bytes))
		if err := s.daemon.Notifier.Notify(ctx, summary, body); err != nil {
			s.daemon.Logger.Debug("desktop notification", "err", err)
		}
	}
}
