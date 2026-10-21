package syncer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
	"golang.org/x/sync/errgroup"
)

// executeBatch runs the batch level by level: folders by depth so parents exist first, then
// content and placement changes in parallel with small files first, then deletes. Progress
// is saved once per level; within a level only resumable-upload state is saved as it
// happens, since every other step is safe to repeat thanks to pre-generated IDs.
func (o *Outgoing) executeBatch(ctx context.Context, plan *BatchPlan) error {
	for _, level := range executionLevels(plan) {
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(o.config.Uploads)
		for _, i := range level {
			o.batchMu.Lock()
			op := plan.Ops[i]
			o.batchMu.Unlock()
			if op.Done {
				continue
			}
			group.Go(func() error {
				step := opStep{outgoing: o, plan: plan, index: i, op: &op}
				if err := o.executeOp(groupCtx, step); err != nil {
					return fmt.Errorf("%s %s: %w", op.Kind, op.ObjectID, err)
				}
				op.Done = true
				return step.record()
			})
		}
		err := group.Wait()
		if saveErr := o.saveBatch(ctx, plan); err == nil {
			err = saveErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func executionLevels(plan *BatchPlan) [][]int {
	folders := map[int][]int{}
	var content, deletes []int
	for i, op := range plan.Ops {
		switch {
		case op.Kind == NetCreate && op.Folder:
			depth := strings.Count(op.Path, "/")
			folders[depth] = append(folders[depth], i)
		case op.Kind == NetDelete || op.Kind == NetAbsorb:
			deletes = append(deletes, i)
		default:
			content = append(content, i)
		}
	}
	depths := make([]int, 0, len(folders))
	for depth := range folders {
		depths = append(depths, depth)
	}
	sort.Ints(depths)
	var levels [][]int
	for _, depth := range depths {
		levels = append(levels, folders[depth])
	}
	sort.SliceStable(content, func(a, b int) bool { return plan.Ops[content[a]].Object.Size < plan.Ops[content[b]].Object.Size })
	return append(levels, content, deletes)
}

// opStep is one op being executed on its own copy, with a way to checkpoint progress.
type opStep struct {
	outgoing *Outgoing
	plan     *BatchPlan
	index    int
	op       *BatchOp
}

// save records the op's progress and persists it at once.
func (s *opStep) save(ctx context.Context) error {
	return s.outgoing.checkpoint(ctx, s.plan, s.index, *s.op)
}

// record writes the op's progress into the plan, to be persisted with the level.
func (s *opStep) record() error {
	s.outgoing.batchMu.Lock()
	defer s.outgoing.batchMu.Unlock()
	s.plan.Ops[s.index] = *s.op
	return nil
}

func (o *Outgoing) executeOp(ctx context.Context, step opStep) error {
	plan, op := step.plan, step.op
	switch {
	case op.Skipped:
		return nil
	case op.Kind == NetCreate && op.Folder:
		return o.createFolder(ctx, plan, op)
	case op.Kind == NetCreate, op.Kind == NetModify:
		return o.uploadContent(ctx, step)
	case op.Kind == NetMove:
		return o.moveRemote(ctx, plan, op)
	case op.Kind == NetAttr:
		_, err := o.Remote.Update(ctx, op.Object.DriveID, RemoteChange{ModifiedTime: op.Object.ModTime})
		return ignoreNotFound(err)
	case op.Kind == NetDelete && op.SubsumedBy == "":
		return o.trashRemote(ctx, step)
	case op.Kind == NetRestore:
		return o.restoreRemote(ctx, plan, op)
	case op.Kind == NetPurge:
		return o.purgeRemote(ctx, op)
	}
	return nil
}

func (o *Outgoing) restoreRemote(ctx context.Context, plan *BatchPlan, op *BatchOp) error {
	parent, err := o.parentDriveID(ctx, plan, op.Object.ParentID)
	if err != nil {
		return err
	}
	return o.placeRemote(ctx, op.RemoteID, op.Object.EncName, parent, map[string]string{"tid": ""})
}

// purgeRemote deletes a trash item. If another device purged it first, nothing is logged,
// so a race between devices leaves one purge in the history, not several.
func (o *Outgoing) purgeRemote(ctx context.Context, op *BatchOp) error {
	if _, err := o.Remote.Get(ctx, op.RemoteID); errors.Is(err, ErrRemoteNotFound) {
		op.Skipped = true
		return nil
	} else if err != nil {
		return err
	}
	return ignoreNotFound(o.Remote.Delete(ctx, op.RemoteID))
}

func (o *Outgoing) createFolder(ctx context.Context, plan *BatchPlan, op *BatchOp) error {
	parent, err := o.parentDriveID(ctx, plan, op.Object.ParentID)
	if err != nil {
		return err
	}
	file, err := o.Remote.CreateFolder(ctx, RemoteSpec{ID: op.NewDriveID, Name: op.Object.EncName, ParentID: parent,
		ModifiedTime: op.Object.ModTime, Properties: o.properties(op.ObjectID)})
	if err != nil {
		return err
	}
	op.Result.DriveID = file.ID
	return nil
}

// moveRemote reads the current parents first, so a replayed move neither fails nor
// duplicates: it only changes what still differs.
func (o *Outgoing) moveRemote(ctx context.Context, plan *BatchPlan, op *BatchOp) error {
	parent, err := o.parentDriveID(ctx, plan, op.Object.ParentID)
	if err != nil {
		return err
	}
	return o.placeRemote(ctx, op.Object.DriveID, op.Object.EncName, parent, nil)
}

func (o *Outgoing) placeRemote(ctx context.Context, driveID, name, parent string, properties map[string]string) error {
	current, err := o.Remote.Get(ctx, driveID)
	if errors.Is(err, ErrRemoteNotFound) {
		o.Logger.Warn("remote object vanished before move", "driveId", driveID)
		return nil
	}
	if err != nil {
		return err
	}
	change := RemoteChange{Properties: properties}
	if current.Name != name {
		change.Name = name
	}
	if !slices.Contains(current.Parents, parent) {
		change.AddParents = []string{parent}
	}
	for _, existing := range current.Parents {
		if existing != parent {
			change.RemoveParents = append(change.RemoveParents, existing)
		}
	}
	if change.Name == "" && len(change.AddParents) == 0 && len(change.RemoveParents) == 0 && properties == nil {
		return nil
	}
	_, err = o.Remote.Update(ctx, driveID, change)
	return ignoreNotFound(err)
}

// trashRemote moves the object, and with a folder its whole subtree, into trash/ under its
// trash ID. A file whose last edits never reached Drive goes to trash as its latest body.
func (o *Outgoing) trashRemote(ctx context.Context, step opStep) error {
	op := step.op
	if op.TrashLatest && !op.Folder {
		return o.uploadContent(ctx, step)
	}
	if op.Object.DriveID == "" {
		return nil
	}
	return o.placeRemote(ctx, op.Object.DriveID, op.TrashID, o.config.TrashFolderID, map[string]string{"tid": op.TrashID})
}

func (o *Outgoing) parentDriveID(ctx context.Context, plan *BatchPlan, parentID model.ObjectID) (string, error) {
	if parentID == model.RootID {
		return o.config.TreeFolderID, nil
	}
	if driveID, ok := o.createdDriveID(plan, parentID); ok {
		return driveID, nil
	}
	var driveID string
	err := o.Index.View(ctx, func(tx *index.Tx) error {
		object, err := tx.Object(parentID)
		driveID = object.DriveID
		return err
	})
	if err == nil && driveID == "" {
		err = fmt.Errorf("parent %s is not on Drive yet", parentID)
	}
	return driveID, err
}

func (o *Outgoing) properties(id model.ObjectID) map[string]string {
	return map[string]string{"vid": o.config.VaultID, "oid": string(id)}
}

func ignoreNotFound(err error) error {
	if errors.Is(err, ErrRemoteNotFound) {
		return nil
	}
	return err
}
