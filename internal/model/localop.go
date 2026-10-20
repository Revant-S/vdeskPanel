package model

// LocalOpKind names what one filesystem call did, before batching reduces ops to net effect.
type LocalOpKind string

const (
	LocalCreate LocalOpKind = "create"
	LocalModify LocalOpKind = "modify"
	LocalAttr   LocalOpKind = "attr"
	LocalMove   LocalOpKind = "move"
	LocalDelete LocalOpKind = "delete"
	// LocalAbsorb removes an object whose body became another object's content through a
	// rename over it. Its remote copy is deleted outright, not trashed.
	LocalAbsorb LocalOpKind = "absorb"
	// LocalSupersede puts a version that lost a conflict into the trash. The object is a
	// hidden holder: either a local body still to upload, or a Drive file already uploaded.
	LocalSupersede LocalOpKind = "supersede"
)

type DeleteReason string

const (
	ReasonDeleted    DeleteReason = "deleted"
	ReasonMovedOut   DeleteReason = "moved out"
	ReasonSuperseded DeleteReason = "superseded"
)

func SupersededBy(device string) DeleteReason {
	return DeleteReason("superseded by " + device)
}

func ParentDeletedOn(device string) DeleteReason {
	return DeleteReason("parent deleted on " + device)
}

// LocalOp is the sealed payload of one pending op. Paths are recorded when the op happens,
// since a deleted object is no longer in the tree when the batch runs.
type LocalOp struct {
	Kind         LocalOpKind  `json:"kind"`
	ObjectID     ObjectID     `json:"oid"`
	Folder       bool         `json:"folder,omitempty"`
	ParentID     ObjectID     `json:"parent,omitempty"`
	Name         string       `json:"name,omitempty"`
	Path         string       `json:"path,omitempty"`
	FromParentID ObjectID     `json:"fromParent,omitempty"`
	FromName     string       `json:"fromName,omitempty"`
	FromPath     string       `json:"fromPath,omitempty"`
	ReplacedFrom ObjectID     `json:"replacedFrom,omitempty"`
	Reason       DeleteReason `json:"reason,omitempty"`
}
