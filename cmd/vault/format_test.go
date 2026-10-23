package main

import (
	"strings"
	"testing"
	"time"

	"vault/internal/api"
)

func TestStatusLinesMatchTheSpec(t *testing.T) {
	now := time.Unix(10000, 0)
	backed := formatStatus(api.Status{State: api.StateMounted, LastBackedUp: now.Add(-2 * time.Minute)}, now)
	pending := formatStatus(api.Status{State: api.StateMounted, NotBackedUpFiles: 3, NotBackedUpBytes: 45_000_000}, now)
	if !strings.Contains(backed, "All backed up · 2 min ago") || !strings.Contains(pending, "3 files not backed up yet · 45 MB") {
		t.Fatalf("got %q and %q", backed, pending)
	}
}

func TestMountPathsBecomeVaultPaths(t *testing.T) {
	for arg, want := range map[string]string{"/home/u/Vault/Photos/a.jpg": "/Photos/a.jpg", "/home/u/Vault": "/", "/Photos": "/Photos"} {
		if got := vaultPath(arg, "/home/u/Vault"); got != want {
			t.Errorf("vaultPath(%q) = %q, want %q", arg, got, want)
		}
	}
}
