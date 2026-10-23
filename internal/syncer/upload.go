package syncer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/cosmoboy/vault/internal/cache"
	"github.com/cosmoboy/vault/internal/crypto"
	"github.com/cosmoboy/vault/internal/index"
	"github.com/cosmoboy/vault/internal/model"
)

var errChecksumMismatch = errors.New("syncer: uploaded body checksum differs from what was sent")

// uploadContent puts the object's current body on Drive as a new file: a server-side copy
// when identical content is already there, otherwise an upload of a fresh snapshot.
func (o *Outgoing) uploadContent(ctx context.Context, step opStep) error {
	op := step.op
	if op.Snapshot == nil {
		if err := o.takeSnapshot(ctx, step); err != nil || op.Skipped {
			return err
		}
	}
	spec, err := o.contentSpec(ctx, step)
	if err != nil {
		return err
	}
	if !o.isTrashUpload(op) {
		if copied, err := o.copyDuplicate(ctx, step, spec); err != nil || copied {
			return err
		}
		spec.ID = op.NewDriveID
	}
	for attempt := 0; attempt < 3; attempt++ {
		remote, err := o.uploadSnapshot(ctx, step, spec)
		if err != nil {
			return err
		}
		if remote.MD5 == op.Snapshot.MD5 {
			op.Result = ContentResult{DriveID: remote.ID, BodyID: op.Snapshot.BodyID, BodyMD5: op.Snapshot.MD5,
				ContentMAC: op.Snapshot.ContentMAC, Size: op.Snapshot.PlainSize}
			return nil
		}
		if err := o.discardBadUpload(ctx, step, remote.ID); err != nil {
			return err
		}
		spec.ID = op.NewDriveID
	}
	return errChecksumMismatch
}

func (o *Outgoing) takeSnapshot(ctx context.Context, step opStep) error {
	op := step.op
	snapshot, err := o.Cache.Snapshot(ctx, step.plan.ID, op.ObjectID)
	if errors.Is(err, cache.ErrCorruptBody) {
		o.Logger.Error("local body failed authentication; keeping the version on Drive", "oid", op.ObjectID)
		op.Skipped = true
		return o.Index.Update(ctx, func(tx *index.Tx) error { return tx.SetLocalState(op.ObjectID, model.Corrupt) })
	}
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	var mac []byte
	err = o.Keyring.Use(func(k *crypto.Keys) error {
		mac = crypto.ContentMAC(k, snapshot.PlaintextSHA256)
		return nil
	})
	if err != nil {
		return err
	}
	op.Snapshot = &SnapshotInfo{Path: snapshot.Path, MD5: hex.EncodeToString(snapshot.CiphertextMD5[:]),
		Size: snapshot.CiphertextSize, PlainSize: snapshot.PlaintextSize, BodyID: snapshot.Header.BodyID[:], ContentMAC: mac}
	op.Session = UploadSession{}
	if op.Snapshot.Size > smallFileLimit {
		return step.save(ctx)
	}
	return nil
}

func (o *Outgoing) isTrashUpload(op *BatchOp) bool {
	return op.Kind == NetDelete
}

func (o *Outgoing) contentSpec(ctx context.Context, step opStep) (RemoteSpec, error) {
	op := step.op
	if o.isTrashUpload(op) {
		properties := o.properties(op.ObjectID)
		properties["tid"] = op.TrashID
		return RemoteSpec{ID: op.NewDriveID, Name: op.TrashID, ParentID: o.config.TrashFolderID,
			ModifiedTime: op.Object.ModTime, Properties: properties}, nil
	}
	parent, err := o.parentDriveID(ctx, step.plan, op.Object.ParentID)
	if err != nil {
		return RemoteSpec{}, err
	}
	properties := o.properties(op.ObjectID)
	properties["ver"] = op.Version.String()
	return RemoteSpec{ID: op.NewDriveID, Name: op.Object.EncName, ParentID: parent, ModifiedTime: op.Object.ModTime,
		Properties: properties}, nil
}

// copyDuplicate makes cp inside the vault, and moving a file back in, free: identical
// content already on Drive is copied server-side instead of uploaded.
func (o *Outgoing) copyDuplicate(ctx context.Context, step opStep, spec RemoteSpec) (bool, error) {
	op := step.op
	source, ok, err := o.findDuplicate(ctx, op.Snapshot.ContentMAC, op.ObjectID)
	if err != nil || !ok {
		return false, err
	}
	remote, err := o.Remote.Copy(ctx, source.DriveID, spec)
	if errors.Is(err, ErrRemoteExists) {
		remote, err = o.Remote.Get(ctx, spec.ID)
	}
	if errors.Is(err, ErrRemoteNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("copy duplicate: %w", err)
	}
	if remote.MD5 != source.BodyMD5 {
		if err := o.discardBadUpload(ctx, step, remote.ID); err != nil {
			return false, err
		}
		return false, nil
	}
	op.Result = ContentResult{DriveID: remote.ID, BodyID: source.BodyID, BodyMD5: source.BodyMD5,
		ContentMAC: op.Snapshot.ContentMAC, Size: op.Snapshot.PlainSize, Copied: true}
	return true, nil
}

func (o *Outgoing) findDuplicate(ctx context.Context, mac []byte, self model.ObjectID) (index.Object, bool, error) {
	var found index.Object
	var ok bool
	err := o.Index.View(ctx, func(tx *index.Tx) error {
		candidates, err := tx.ObjectsByContentMAC(mac)
		for _, candidate := range candidates {
			if candidate.ID != self && candidate.DriveID != "" && candidate.BodyMD5 != "" && len(candidate.BodyID) > 0 {
				found, ok = candidate, true
				return nil
			}
		}
		return err
	})
	return found, ok, err
}

func (o *Outgoing) uploadSnapshot(ctx context.Context, step opStep, spec RemoteSpec) (model.RemoteFile, error) {
	op := step.op
	file, err := os.Open(op.Snapshot.Path)
	if errors.Is(err, os.ErrNotExist) {
		op.Snapshot, op.Session = nil, UploadSession{}
		if saveErr := step.save(ctx); saveErr != nil {
			return model.RemoteFile{}, saveErr
		}
		return model.RemoteFile{}, fmt.Errorf("snapshot vanished; will snapshot again: %w", err)
	}
	if err != nil {
		return model.RemoteFile{}, err
	}
	defer file.Close()
	if op.Snapshot.Size <= smallFileLimit {
		remote, err := o.Remote.CreateSmallFile(ctx, spec, file, op.Snapshot.Size)
		if errors.Is(err, ErrRemoteExists) {
			return o.Remote.Get(ctx, spec.ID)
		}
		return remote, err
	}
	return o.uploadResumable(ctx, step, spec, file)
}

func (o *Outgoing) uploadResumable(ctx context.Context, step opStep, spec RemoteSpec, file *os.File) (model.RemoteFile, error) {
	op := step.op
	for attempt := 0; attempt < 2; attempt++ {
		if op.Session.URI == "" {
			session, err := o.Remote.StartResumableUpload(ctx, spec, op.Snapshot.Size)
			if errors.Is(err, ErrRemoteExists) {
				return o.Remote.Get(ctx, spec.ID)
			}
			if err != nil {
				return model.RemoteFile{}, err
			}
			op.Session = session
			if err := step.save(ctx); err != nil {
				return model.RemoteFile{}, err
			}
		}
		remote, err := o.Remote.ResumeUpload(ctx, op.Session, file, nil)
		if isServerError(err) {
			return model.RemoteFile{}, o.noteSessionFailure(ctx, step, err)
		}
		if !errors.Is(err, ErrUploadSessionExpired) {
			return remote, err
		}
		op.Session = UploadSession{}
	}
	return model.RemoteFile{}, ErrUploadSessionExpired
}

// discardBadUpload removes a body that is not the snapshot just taken, either because Drive
// stored it wrongly or because it is from before a crash, and arranges a fresh ID for a
// clean new upload.
func (o *Outgoing) discardBadUpload(ctx context.Context, step opStep, driveID string) error {
	if err := ignoreNotFound(o.Remote.Delete(ctx, driveID)); err != nil {
		return err
	}
	ids, err := o.Remote.GenerateIDs(ctx, 1)
	if err != nil {
		return err
	}
	step.op.NewDriveID, step.op.Session = ids[0], UploadSession{}
	return step.save(ctx)
}

const maxSessionFailures = 2

// noteSessionFailure counts server errors on an upload session and, when they repeat,
// abandons the session and its reserved ID so the next try starts a clean upload.
func (o *Outgoing) noteSessionFailure(ctx context.Context, step opStep, cause error) error {
	op := step.op
	op.SessionFailures++
	if op.SessionFailures < maxSessionFailures {
		return errors.Join(cause, step.save(ctx))
	}
	ids, err := o.Remote.GenerateIDs(ctx, 1)
	if err != nil {
		return errors.Join(cause, err)
	}
	o.Logger.Warn("abandoning an upload session Drive keeps failing", "oid", op.ObjectID)
	op.Session, op.NewDriveID, op.SessionFailures = UploadSession{}, ids[0], 0
	return errors.Join(cause, step.save(ctx))
}
