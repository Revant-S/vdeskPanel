// Package syncer moves changes between the local workspace and Drive: it batches local ops,
// reduces them to net effect, uploads, commits to the log, and applies remote commits.
package syncer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"vault/internal/index"
	"vault/internal/model"
)

type NetKind string

const (
	NetCreate NetKind = "create"
	NetModify NetKind = "modify"
	NetAttr   NetKind = "attr"
	NetMove   NetKind = "move"
	NetDelete NetKind = "delete"
	// NetAbsorb deletes the remote copy of an object whose body moved into another object.
	NetAbsorb NetKind = "absorb"
	// NetDropLocal forgets an object that never reached Drive.
	NetDropLocal NetKind = "drop_local"
	// NetNoop consumes ops that cancelled out, such as a move there and back.
	NetNoop    NetKind = "noop"
	NetRestore NetKind = "restore"
	NetPurge   NetKind = "purge"
)

// NetOp is what a batch does to one object, after its raw ops cancel or merge.
type NetOp struct {
	Kind        NetKind            `json:"kind"`
	ObjectID    model.ObjectID     `json:"oid"`
	Folder      bool               `json:"folder,omitempty"`
	Path        string             `json:"path,omitempty"`
	FromPath    string             `json:"fromPath,omitempty"`
	Moved       bool               `json:"moved,omitempty"`
	Reason      model.DeleteReason `json:"reason,omitempty"`
	TrashLatest bool               `json:"trashLatest,omitempty"`
	AdoptFrom   model.ObjectID     `json:"adoptFrom,omitempty"`
	SubsumedBy  model.ObjectID     `json:"subsumedBy,omitempty"`
	Superseded  bool               `json:"superseded,omitempty"`
	FileCount   int                `json:"fileCount,omitempty"`
	Version     model.Stamp        `json:"version"`
	Seqs        []int64            `json:"seqs"`
	TrashItem   string             `json:"trashItem,omitempty"`
	RemoteID    string             `json:"remoteId,omitempty"`
}

// RemoteFacts tells the reducer what Drive already holds for an object before this batch.
type RemoteFacts func(id model.ObjectID) (synced bool)

type objectOps struct {
	id  model.ObjectID
	ops []model.LocalOp
	raw []index.PendingOp
}

// ReduceToNetEffect turns raw ops into one net op per object (two when content and placement
// both changed), then applies the cross-object rules: save-by-rename adoption and folder
// delete coalescing.
func ReduceToNetEffect(raw []index.PendingOp, synced RemoteFacts) ([]NetOp, error) {
	groups, err := groupByObject(raw)
	if err != nil {
		return nil, err
	}
	var net []NetOp
	for _, group := range groups {
		net = append(net, reduceObject(group, synced(group.id))...)
	}
	net = adoptReplacements(net, groups, synced)
	net = coalesceFolderDeletes(net)
	sort.SliceStable(net, func(a, b int) bool { return net[a].Seqs[0] < net[b].Seqs[0] })
	return net, nil
}

func groupByObject(raw []index.PendingOp) ([]*objectOps, error) {
	byID := map[model.ObjectID]*objectOps{}
	var order []*objectOps
	for _, op := range raw {
		var local model.LocalOp
		if err := json.Unmarshal(op.Payload, &local); err != nil {
			return nil, fmt.Errorf("decode op %d: %w", op.Seq, err)
		}
		group := byID[op.ObjectID]
		if group == nil {
			group = &objectOps{id: op.ObjectID}
			byID[op.ObjectID] = group
			order = append(order, group)
		}
		group.ops, group.raw = append(group.ops, local), append(group.raw, op)
	}
	return order, nil
}

func reduceObject(group *objectOps, synced bool) []NetOp {
	if kind := group.ops[len(group.ops)-1].Kind; kind == model.LocalRestore || kind == model.LocalPurge {
		return trashNetOps(group)
	}
	var created, modified, attr, moved, deleted, absorbed, superseded bool
	var firstFrom string
	var last model.LocalOp
	for _, op := range group.ops {
		switch op.Kind {
		case model.LocalCreate:
			created = true
		case model.LocalModify:
			modified = true
		case model.LocalAttr:
			attr = true
		case model.LocalMove:
			if !moved {
				firstFrom = op.FromPath
			}
			moved = true
		case model.LocalDelete:
			deleted = true
		case model.LocalAbsorb:
			absorbed = true
		case model.LocalSupersede:
			superseded = true
		}
		last = op
	}
	base := NetOp{ObjectID: group.id, Folder: group.ops[0].Folder, Path: last.Path, Seqs: seqsOf(group.raw),
		Version: group.raw[len(group.raw)-1].Stamp}
	switch {
	case superseded:
		op := with(base, NetDelete)
		op.Reason, op.TrashLatest, op.Superseded = last.Reason, !synced, true
		return []NetOp{op}
	case absorbed || (deleted && !synced) || (created && !synced && deleted):
		if synced {
			return []NetOp{with(base, NetAbsorb)}
		}
		return []NetOp{with(base, NetDropLocal)}
	case deleted:
		op := with(base, NetDelete)
		op.Reason, op.TrashLatest, op.FromPath = last.Reason, modified, firstFrom
		return []NetOp{op}
	case created && !synced:
		return []NetOp{with(base, NetCreate)}
	}
	var out []NetOp
	movedAway := moved && firstFrom != last.Path
	if modified {
		op := with(base, NetModify)
		op.Moved, op.FromPath = movedAway, firstFrom
		return append(out, op)
	}
	if movedAway {
		op := with(base, NetMove)
		op.FromPath = firstFrom
		out = append(out, op)
	}
	if attr {
		out = append(out, with(base, NetAttr))
	}
	if len(out) == 0 {
		out = append(out, with(base, NetNoop))
	}
	return out
}

func with(op NetOp, kind NetKind) NetOp {
	op.Kind = kind
	return op
}

func seqsOf(raw []index.PendingOp) []int64 {
	seqs := make([]int64, len(raw))
	for i, op := range raw {
		seqs[i] = op.Seq
	}
	return seqs
}

// adoptReplacements recognises a save that moved the old file aside, wrote a new one in its
// place and deleted the old: the new file takes over the old file's identity.
func adoptReplacements(net []NetOp, groups []*objectOps, synced RemoteFacts) []NetOp {
	originalPath := map[model.ObjectID]string{}
	for _, group := range groups {
		originalPath[group.id] = pathBeforeBatch(group.ops)
	}
	deletedAt := map[string]int{}
	for i, op := range net {
		if op.Kind == NetDelete && !op.Folder && op.Reason != model.ReasonMovedOut {
			deletedAt[originalPath[op.ObjectID]] = i
		}
	}
	for i, op := range net {
		if op.Kind != NetCreate || op.Folder || synced(op.ObjectID) {
			continue
		}
		j, ok := deletedAt[op.Path]
		if !ok {
			continue
		}
		replaced := net[j]
		net[j] = NetOp{ObjectID: replaced.ObjectID, Kind: NetModify, AdoptFrom: op.ObjectID, Path: op.Path,
			Version: op.Version, Seqs: append(replaced.Seqs, op.Seqs...)}
		net[i].Kind = ""
		delete(deletedAt, op.Path)
	}
	return withoutEmpty(net)
}

func pathBeforeBatch(ops []model.LocalOp) string {
	for _, op := range ops {
		switch op.Kind {
		case model.LocalMove:
			return op.FromPath
		case model.LocalCreate:
			return ""
		}
	}
	return ops[len(ops)-1].Path
}

// coalesceFolderDeletes folds deletes under a deleted folder into the folder's one trash entry.
func coalesceFolderDeletes(net []NetOp) []NetOp {
	var folders []int
	for i, op := range net {
		if op.Kind == NetDelete && op.Folder {
			folders = append(folders, i)
		}
	}
	sort.Slice(folders, func(a, b int) bool { return len(net[folders[a]].Path) < len(net[folders[b]].Path) })
	movedOut := map[int]bool{}
	for _, i := range folders {
		movedOut[i] = true
	}
	for i := range net {
		if net[i].Kind != NetDelete && net[i].Kind != NetDropLocal {
			continue
		}
		for _, f := range folders {
			if f == i || net[f].SubsumedBy != "" || !strings.HasPrefix(net[i].Path, net[f].Path+"/") {
				continue
			}
			net[i].SubsumedBy = net[f].ObjectID
			if !net[i].Folder {
				net[f].FileCount++
				movedOut[f] = movedOut[f] && net[i].Reason == model.ReasonMovedOut
			}
			break
		}
	}
	for _, f := range folders {
		if net[f].FileCount > 0 && movedOut[f] {
			net[f].Reason = model.ReasonMovedOut
		}
	}
	return net
}

func withoutEmpty(net []NetOp) []NetOp {
	out := net[:0]
	for _, op := range net {
		if op.Kind != "" {
			out = append(out, op)
		}
	}
	return out
}

// trashNetOps keeps restores and purges as they are: each one is a single Drive call on a
// trash item, already reflected locally.
func trashNetOps(group *objectOps) []NetOp {
	var out []NetOp
	for i, op := range group.ops {
		kind := NetRestore
		if op.Kind == model.LocalPurge {
			kind = NetPurge
		}
		out = append(out, NetOp{Kind: kind, ObjectID: group.id, Folder: op.Folder, Path: op.Path, TrashItem: op.TrashID,
			RemoteID: op.DriveID, Version: group.raw[i].Stamp, Seqs: []int64{group.raw[i].Seq}})
	}
	return out
}
