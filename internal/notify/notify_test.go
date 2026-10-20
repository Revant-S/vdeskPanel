package notify

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type fakeNotificationServer struct {
	mu       sync.Mutex
	received []string
}

func (s *fakeNotificationServer) Notify(appName string, _ uint32, _, summary, body string, _ []string, _ map[string]dbus.Variant, _ int32) (uint32, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received = append(s.received, appName+"|"+summary+"|"+body)
	return uint32(len(s.received)), nil
}

func startPrivateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not installed")
	}
	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--print-address")
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Skipf("start dbus-daemon: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	address, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read bus address: %v", err)
	}
	return strings.TrimSpace(address)
}

func connect(t *testing.T, address string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestNotifyReachesNotificationServer(t *testing.T) {
	address := startPrivateBus(t)
	server := &fakeNotificationServer{}
	serverConn := connect(t, address)
	if err := serverConn.Export(server, "/org/freedesktop/Notifications", "org.freedesktop.Notifications"); err != nil {
		t.Fatal(err)
	}
	if reply, err := serverConn.RequestName("org.freedesktop.Notifications", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request name: %v %v", reply, err)
	}
	desktop := NewDesktop(connect(t, address))
	summary, body := FileMovedOut("report.pdf", 30)
	if err := desktop.Notify(context.Background(), summary, body); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.mu.Lock()
		got := append([]string(nil), server.received...)
		server.mu.Unlock()
		if len(got) == 1 {
			want := "Vault|Vault|report.pdf left the vault and is no longer tracked. Kept in trash for 30 days."
			if got[0] != want {
				t.Fatalf("got %q", got[0])
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("notification never arrived")
}

func TestMessagesMatchSpec(t *testing.T) {
	if _, body := FolderMovedOut("Photos", 412, 30); !strings.HasPrefix(body, "Photos/ (412 files) left the vault") {
		t.Fatalf("folder message = %q", body)
	}
}
