package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

type fakeService struct {
	Service
	password []byte
	events   chan Event
}

func (f *fakeService) Status(context.Context) (Status, error) {
	return Status{State: StateMounted, NotBackedUpFiles: 3}, nil
}

func (f *fakeService) Unlock(_ context.Context, password []byte) error {
	f.password = append([]byte(nil), password...)
	if string(password) != "correct horse battery" {
		return ErrWrongPassword
	}
	return nil
}

func (f *fakeService) RestoreTrash(_ context.Context, id string) (string, error) {
	if id != "t-1" {
		return "", ErrNotFound
	}
	return "/report (restored).pdf", nil
}

func (f *fakeService) Subscribe(ctx context.Context) <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		for {
			select {
			case event := <-f.events:
				out <- event
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func serveFake(t *testing.T) (*fakeService, *Client) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "run", "daemon.sock")
	listener, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeService{events: make(chan Event, 4)}
	server := NewServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go server.Serve(listener)
	t.Cleanup(func() { server.Shutdown(context.Background()) })
	return service, NewClient(socket)
}

func TestClientAndServerRoundTrip(t *testing.T) {
	service, client := serveFake(t)
	ctx := context.Background()
	status, err := client.Status(ctx)
	if err != nil || status.State != StateMounted || status.NotBackedUpFiles != 3 {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if err := client.Unlock(ctx, []byte("wrong")); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password error = %v", err)
	}
	if err := client.Unlock(ctx, []byte("correct horse battery")); err != nil || string(service.password) != "correct horse battery" {
		t.Fatalf("unlock = %v, daemon got %q", err, service.password)
	}
	if path, err := client.RestoreTrash(ctx, "t-1"); err != nil || path != "/report (restored).pdf" {
		t.Fatalf("restore = %q, %v", path, err)
	}
	if _, err := client.RestoreTrash(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing trash error = %v", err)
	}
	if err := client.Unlock(ctx, make([]byte, 2048)); !errors.Is(err, ErrInvalidRequest) && err == nil {
		t.Fatal("oversized password accepted")
	}
}

func TestEventsStreamToClient(t *testing.T) {
	service, client := serveFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := client.Subscribe(ctx)
	time.Sleep(100 * time.Millisecond)
	service.events <- Event{Kind: EventStatus, Status: &Status{State: StateLocked}}
	select {
	case event := <-events:
		if event.Kind != EventStatus || event.Status.State != StateLocked {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event arrived")
	}
}
