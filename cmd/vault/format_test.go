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

func TestStatusSaysOffline(t *testing.T) {
	if got := formatStatus(api.Status{State: api.StateMounted}, time.Now()); !strings.Contains(got, "Offline") {
		t.Fatalf("got %q", got)
	}
}

func TestSettingsAssignments(t *testing.T) {
	settings := api.Settings{Uploads: 4}
	for _, arg := range []string{"uploads=8", "device_name=work laptop", "thumbnailer_deny_list=tracker-miner-fs-3,totem"} {
		if err := applySetting(&settings, arg); err != nil {
			t.Fatal(err)
		}
	}
	if settings.Uploads != 8 || settings.DeviceName != "work laptop" || len(settings.ThumbnailerDenyList) != 2 {
		t.Fatalf("settings = %+v", settings)
	}
	for _, bad := range []string{"uploads", "uploads=many", "colour=blue"} {
		if applySetting(&settings, bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestHistoryPrintsOpsAndNextPage(t *testing.T) {
	var out strings.Builder
	printHistory(&out, api.CommitPage{NextBefore: "c1", Commits: []api.CommitView{{DeviceName: "amber", Summary: "1 moved",
		Ops: []api.OpView{{Kind: "move", From: "/a", To: "/b"}}}}}, true)
	if !strings.Contains(out.String(), "/a -> /b") || !strings.Contains(out.String(), "--before c1") {
		t.Fatalf("got %q", out.String())
	}
}
