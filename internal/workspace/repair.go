package workspace

import (
	"context"
	"fmt"

	"vault/internal/index"
	"vault/internal/model"
)

const recoveredFolderName = "Recovered"

// repairOrphans finds live objects whose parent is deleted or missing and moves them into a
// top-level "Recovered" folder, so nothing becomes unreachable and they sync again. Older
// builds could leave such orphans when a folder was moved to the desktop trash.
func (w *Workspace) repairOrphans(ctx context.Context) (int, error) {
	var repaired int
	err := w.update(ctx, func(tx *index.Tx) error {
		orphans, err := orphanedObjects(tx)
		if err != nil || len(orphans) == 0 {
			return err
		}
		folder, err := w.createRecoveredFolder(tx)
		if err != nil {
			return err
		}
		taken := map[string]bool{}
		for _, orphan := range orphans {
			if err := w.reattach(tx, orphan, folder, taken); err != nil {
				return err
			}
		}
		repaired = len(orphans)
		return nil
	})
	if repaired > 0 {
		w.Logger.Warn("moved orphaned objects into /Recovered", "count", repaired)
	}
	return repaired, err
}

func orphanedObjects(tx *index.Tx) ([]index.Object, error) {
	live := map[model.ObjectID]bool{model.RootID: true}
	var all []index.Object
	for object, err := range tx.AllObjects() {
		if err != nil {
			return nil, err
		}
		if isLive(object) {
			live[object.ID] = true
			all = append(all, object)
		}
	}
	var orphans []index.Object
	for _, object := range all {
		if !live[object.ParentID] {
			orphans = append(orphans, object)
		}
	}
	return orphans, nil
}

func (w *Workspace) createRecoveredFolder(tx *index.Tx) (index.Object, error) {
	id, err := model.NewObjectID(w.Random)
	if err != nil {
		return index.Object{}, err
	}
	name := fmt.Sprintf("%s %s", recoveredFolderName, w.Now().Format("2006-01-02 15.04"))
	encName, err := w.Names.EncryptName(id, name)
	if err != nil {
		return index.Object{}, err
	}
	stamp := w.Clock.Next()
	folder := index.Object{ID: id, ParentID: model.RootID, Kind: model.KindFolder, EncName: encName, ModTime: w.Now(),
		State: model.Local, ContentVer: stamp, PlacementVer: stamp}
	if err := tx.PutObject(folder); err != nil {
		return index.Object{}, err
	}
	op := model.LocalOp{Kind: model.LocalCreate, ObjectID: id, Folder: true, ParentID: model.RootID, Name: name, Path: "/" + name}
	return folder, appendOp(tx, op, stamp, w.Now())
}

func (w *Workspace) reattach(tx *index.Tx, orphan, folder index.Object, taken map[string]bool) error {
	name, err := w.Names.DecryptName(orphan.ID, orphan.EncName)
	if err != nil {
		return err
	}
	unique := name
	for n := 2; taken[unique]; n++ {
		unique = fmt.Sprintf("%s (%d)", name, n)
	}
	taken[unique] = true
	if orphan.EncName, err = w.Names.EncryptName(orphan.ID, unique); err != nil {
		return err
	}
	folderName, _ := w.Names.DecryptName(folder.ID, folder.EncName)
	stamp := w.Clock.Next()
	from := orphan.ParentID
	orphan.ParentID, orphan.PlacementVer = folder.ID, stamp
	if err := tx.PutObject(orphan); err != nil {
		return err
	}
	op := model.LocalOp{Kind: model.LocalMove, ObjectID: orphan.ID, Folder: orphan.Kind == model.KindFolder, ParentID: folder.ID,
		Name: unique, Path: "/" + folderName + "/" + unique, FromParentID: from, FromName: name, FromPath: "/?/" + name}
	return appendOp(tx, op, stamp, w.Now())
}
