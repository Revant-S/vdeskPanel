package syncer_test

import (
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"testing"

	"vault/internal/devicetest"
	"vault/internal/model"
)

// treeOf lists every path on a device with its content, or "/" for a folder.
func treeOf(t *testing.T, d *devicetest.Device) map[string]string {
	t.Helper()
	out := map[string]string{}
	var walk func(id model.ObjectID, dir string)
	walk = func(id model.ObjectID, dir string) {
		for _, entry := range d.Workspace.Tree().Children(id) {
			p := path.Join(dir, entry.Name)
			if entry.IsFolder() {
				out[p] = "/"
				walk(entry.ID, p)
				continue
			}
			out[p] = string(d.ReadFile(p))
		}
	}
	walk(model.RootID, "/")
	return out
}

func describeTree(tree map[string]string) string {
	var lines []string
	for _, p := range slices.Sorted(maps.Keys(tree)) {
		lines = append(lines, fmt.Sprintf("%s=%q", p, tree[p]))
	}
	return strings.Join(lines, " ")
}

// settle syncs and polls every device until a few full rounds have passed, enough for every
// conflict fix-up commit to reach every device.
func settle(t *testing.T, devices ...*devicetest.Device) {
	t.Helper()
	for range 4 {
		for _, d := range devices {
			d.SyncAndPoll()
		}
	}
}

// assertConverged settles the devices, then checks they and a newly joined device all hold
// the same tree, the same trash and no backlog. It returns the common tree.
func assertConverged(t *testing.T, n *devicetest.Network, devices ...*devicetest.Device) map[string]string {
	t.Helper()
	settle(t, devices...)
	joined := n.NewDevice(t, fmt.Sprintf("joined-%d", len(n.Drive.Requests())))
	joined.Poll()
	want := treeOf(t, devices[0])
	wantTrash := trashReasons(t, devices[0])
	for _, d := range append(devices[1:], joined) {
		if got := treeOf(t, d); !maps.Equal(got, want) {
			t.Fatalf("trees diverge:\n%s: %s\n%s: %s", devices[0].ID, describeTree(want), d.ID, describeTree(got))
		}
		if got := trashReasons(t, d); !slices.Equal(got, wantTrash) {
			t.Fatalf("trash diverges:\n%s: %v\n%s: %v", devices[0].ID, wantTrash, d.ID, got)
		}
	}
	for _, d := range devices {
		if files, _, err := d.Outgoing.Backlog(context.Background()); err != nil || files != 0 {
			t.Fatalf("%s: backlog %d files (%v)", d.ID, files, err)
		}
	}
	return want
}

func expectTree(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	if !maps.Equal(got, want) {
		t.Fatalf("tree = %s\nwant   %s", describeTree(got), describeTree(want))
	}
}
