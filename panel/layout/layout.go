// Package layout places History commits on device lanes for the panel's SVG graph. It is
// pure so the graph can be tested without a window.
package layout

import (
	"cmp"
	"slices"

	"vault/internal/api"
)

// PaletteSize is the number of lane colours the frontend defines.
const PaletteSize = 8

// OffPage marks an edge whose parent is not among the commits being laid out.
const OffPage = -1

// UnknownLane marks an off-page merge parent whose device cannot be known yet.
const UnknownLane = -1

type Edge struct {
	Parent string `json:"parent"`
	ToRow  int    `json:"toRow"`
	ToLane int    `json:"toLane"`
	Merge  bool   `json:"merge"`
}

type Row struct {
	Commit  api.CommitView `json:"commit"`
	Lane    int            `json:"lane"`
	Colour  int            `json:"colour"`
	Retired bool           `json:"retired"`
	Merge   bool           `json:"merge"`
	Edges   []Edge         `json:"edges"`
}

type lane struct {
	index   int
	retired bool
}

// AssignLanes keeps the daemon's order (newest first) and gives every device the same lane
// on every page: lanes follow the device list sorted by first seen, which each page carries
// in full. Devices missing from that list get lanes after it, in order of appearance.
func AssignLanes(commits []api.CommitView, devices []api.DeviceView) []Row {
	lanes := lanesByDevice(commits, devices)
	rowOf := make(map[string]int, len(commits))
	for i, commit := range commits {
		rowOf[commit.ID] = i
	}
	rows := make([]Row, len(commits))
	for i, commit := range commits {
		own := lanes[commit.Device]
		row := Row{Commit: commit, Lane: own.index, Colour: own.index % PaletteSize, Retired: own.retired}
		for position, parent := range commit.Parents {
			edge := edgeTo(parent, position, own.index, rowOf, commits, lanes)
			row.Merge = row.Merge || edge.Merge
			row.Edges = append(row.Edges, edge)
		}
		rows[i] = row
	}
	return rows
}

func lanesByDevice(commits []api.CommitView, devices []api.DeviceView) map[string]lane {
	ordered := slices.Clone(devices)
	slices.SortStableFunc(ordered, func(a, b api.DeviceView) int {
		if byTime := a.FirstSeen.Compare(b.FirstSeen); byTime != 0 {
			return byTime
		}
		return cmp.Compare(a.ID, b.ID)
	})
	lanes := make(map[string]lane, len(ordered))
	for _, device := range ordered {
		if _, seen := lanes[device.ID]; !seen {
			lanes[device.ID] = lane{index: len(lanes), retired: device.Retired}
		}
	}
	for _, commit := range commits {
		if _, seen := lanes[commit.Device]; !seen {
			lanes[commit.Device] = lane{index: len(lanes)}
		}
	}
	return lanes
}

// edgeTo resolves one parent. Off the page, the first parent is by spec this device's own
// previous commit, so it stays in its lane; any other parent is another device's.
func edgeTo(parent string, position, ownLane int, rowOf map[string]int, commits []api.CommitView, lanes map[string]lane) Edge {
	parentRow, onPage := rowOf[parent]
	if !onPage {
		if position == 0 {
			return Edge{Parent: parent, ToRow: OffPage, ToLane: ownLane}
		}
		return Edge{Parent: parent, ToRow: OffPage, ToLane: UnknownLane, Merge: true}
	}
	parentLane := lanes[commits[parentRow].Device].index
	return Edge{Parent: parent, ToRow: parentRow, ToLane: parentLane, Merge: parentLane != ownLane}
}
