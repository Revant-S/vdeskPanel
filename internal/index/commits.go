package index

import (
	"encoding/json"
	"fmt"

	"github.com/cosmoboy/vault/internal/model"
)

// CommitRecord stores a log commit as the sealed blob from Drive plus the clear fields
// needed to order and page it without decrypting.
type CommitRecord struct {
	ID         string
	Device     model.DeviceID
	Clock      model.Stamp
	Parents    []string
	Sealed     []byte
	Applied    bool
	Own        bool
	Referenced bool
}

const commitColumns = `id, device, clock, parents, sealed, applied, own, referenced`

// PutCommit inserts a commit once; a second insert of the same id is ignored.
func (t *Tx) PutCommit(c CommitRecord) error {
	parents, err := json.Marshal(c.Parents)
	if err != nil {
		return err
	}
	_, err = t.exec(`INSERT OR IGNORE INTO commits (`+commitColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Device, c.Clock.String(), string(parents), c.Sealed, boolInt(c.Applied), boolInt(c.Own), boolInt(c.Referenced))
	if err != nil {
		return fmt.Errorf("put commit: %w", err)
	}
	return nil
}

func (t *Tx) Commit(id string) (CommitRecord, error) {
	commits, err := t.selectCommits(`WHERE id = ?`, id)
	if err != nil {
		return CommitRecord{}, err
	}
	if len(commits) == 0 {
		return CommitRecord{}, fmt.Errorf("commit %s: %w", id, ErrNotFound)
	}
	return commits[0], nil
}

// CommitsBefore pages history newest first. A zero stamp starts from the newest commit.
func (t *Tx) CommitsBefore(before model.Stamp, limit int) ([]CommitRecord, error) {
	if before.IsZero() {
		return t.selectCommits(`ORDER BY clock DESC LIMIT ?`, limit)
	}
	return t.selectCommits(`WHERE clock < ? ORDER BY clock DESC LIMIT ?`, before.String(), limit)
}

func (t *Tx) UnappliedCommits() ([]CommitRecord, error) {
	return t.selectCommits(`WHERE applied = 0 ORDER BY clock`)
}

func (t *Tx) LatestCommitOf(device model.DeviceID) (CommitRecord, error) {
	commits, err := t.selectCommits(`WHERE device = ? ORDER BY clock DESC LIMIT 1`, device)
	if err != nil {
		return CommitRecord{}, err
	}
	if len(commits) == 0 {
		return CommitRecord{}, ErrNotFound
	}
	return commits[0], nil
}

// UnreferencedHeads returns, per other device, its newest applied commit not yet named as a parent.
func (t *Tx) UnreferencedHeads(self model.DeviceID) ([]CommitRecord, error) {
	return t.selectCommits(`WHERE device != ? AND applied = 1 AND referenced = 0 AND clock = (
		SELECT MAX(clock) FROM commits AS newer WHERE newer.device = commits.device AND newer.applied = 1)
		ORDER BY device`, self)
}

func (t *Tx) CountCommitsOf(device model.DeviceID) (int, error) {
	var count int
	err := t.queryRow(`SELECT COUNT(*) FROM commits WHERE device = ?`, device).Scan(&count)
	return count, err
}

func (t *Tx) MarkCommitApplied(id string) error {
	_, err := t.exec(`UPDATE commits SET applied = 1 WHERE id = ?`, id)
	return err
}

// MarkDeviceReferenced marks every applied commit of device up to clock as seen by a local commit.
func (t *Tx) MarkDeviceReferenced(device model.DeviceID, upTo model.Stamp) error {
	_, err := t.exec(`UPDATE commits SET referenced = 1 WHERE device = ? AND clock <= ?`, device, upTo.String())
	return err
}

func (t *Tx) selectCommits(where string, args ...any) ([]CommitRecord, error) {
	rows, err := t.query(`SELECT `+commitColumns+` FROM commits `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("select commits: %w", err)
	}
	defer rows.Close()
	var commits []CommitRecord
	for rows.Next() {
		var c CommitRecord
		var clock, parents string
		var applied, own, referenced int
		if err := rows.Scan(&c.ID, &c.Device, &clock, &parents, &c.Sealed, &applied, &own, &referenced); err != nil {
			return nil, err
		}
		if c.Clock, err = model.ParseStamp(clock); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(parents), &c.Parents); err != nil {
			return nil, err
		}
		c.Applied, c.Own, c.Referenced = applied == 1, own == 1, referenced == 1
		commits = append(commits, c)
	}
	return commits, rows.Err()
}
