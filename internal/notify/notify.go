// Package notify shows desktop notifications through org.freedesktop.Notifications, so
// they appear even when the panel is closed.
package notify

import (
	"context"
	"fmt"
	"time"

	"github.com/esiqveland/notify"
	"github.com/godbus/dbus/v5"
)

const displayTime = 10 * time.Second

type Desktop struct {
	conn *dbus.Conn
}

func ConnectSessionBus() (*Desktop, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connect to session bus: %w", err)
	}
	return &Desktop{conn: conn}, nil
}

func NewDesktop(conn *dbus.Conn) *Desktop {
	return &Desktop{conn: conn}
}

func (d *Desktop) Notify(_ context.Context, summary, body string) error {
	_, err := notify.SendNotification(d.conn, notify.Notification{
		AppName:       "Vault",
		AppIcon:       "drive-harddisk",
		Summary:       summary,
		Body:          body,
		ExpireTimeout: displayTime,
	})
	if err != nil {
		return fmt.Errorf("send notification: %w", err)
	}
	return nil
}

func (d *Desktop) Close() error {
	return d.conn.Close()
}

func FileMovedOut(name string, retentionDays int) (summary, body string) {
	return "Vault", fmt.Sprintf("%s left the vault and is no longer tracked. Kept in trash for %d days.", name, retentionDays)
}

func FolderMovedOut(name string, files, retentionDays int) (summary, body string) {
	return "Vault", fmt.Sprintf("%s/ (%d files) left the vault. Kept in trash for %d days.", name, files, retentionDays)
}

func NotBackedUp(files int, bytes string) (summary, body string) {
	return "Vault: not backed up", fmt.Sprintf("%d files not backed up for 10 minutes · %s", files, bytes)
}

func ParentDeletedElsewhere(path, device string) (summary, body string) {
	return "Vault", fmt.Sprintf("%s was in a folder deleted on %s. It is in the trash.", path, device)
}
