package tree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	mathrand "math/rand/v2"
	"strings"
	"testing"

	"vault/internal/model"
)

func encryptTestName(name string) string { return "enc:" + name }

func decryptTestName(_ model.ObjectID, encrypted string) (string, error) {
	name, ok := strings.CutPrefix(encrypted, "enc:")
	if !ok {
		return "", errors.New("bad ciphertext")
	}
	return name, nil
}

func rowsOf(rows []EncryptedEntry) iter.Seq2[EncryptedEntry, error] {
	return func(yield func(EncryptedEntry, error) bool) {
		for _, row := range rows {
			if !yield(row, nil) {
				return
			}
		}
	}
}

// generateRows makes a random but seeded tree: every row's parent is an earlier folder.
func generateRows(count int, seed uint64) []EncryptedEntry {
	random := mathrand.New(mathrand.NewPCG(seed, 1))
	folders := []model.ObjectID{model.RootID}
	rows := make([]EncryptedEntry, 0, count)
	for i := range count {
		id := model.ObjectID(fmt.Sprintf("%032x", i+1))
		parent := folders[random.IntN(len(folders))]
		row := EncryptedEntry{ID: id, ParentID: parent, EncName: encryptTestName(fmt.Sprintf("n%d", i)), Kind: model.KindFile, Size: int64(i), State: model.CloudOnly}
		if random.IntN(5) == 0 {
			row.Kind = model.KindFolder
			folders = append(folders, id)
		}
		rows = append(rows, row)
	}
	return rows
}

func describeTree(t *testing.T, tr *Tree) string {
	t.Helper()
	var out strings.Builder
	stack := []model.ObjectID{model.RootID}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		path, err := tr.PathOf(id)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "%s=%s\n", path, id)
		for _, child := range tr.Children(id) {
			stack = append(stack, child.ID)
		}
	}
	return out.String()
}

func TestParallelBuildIsDeterministic(t *testing.T) {
	rows := generateRows(10000, 7)
	first, err := Build(context.Background(), rowsOf(rows), decryptTestName, 8)
	if err != nil {
		t.Fatal(err)
	}
	if first.Len() != 10001 {
		t.Fatalf("Len = %d", first.Len())
	}
	want := describeTree(t, first)
	for round := range 3 {
		shuffled := append([]EncryptedEntry(nil), rows...)
		mathrand.New(mathrand.NewPCG(uint64(round), 9)).Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})
		again, err := Build(context.Background(), rowsOf(shuffled), decryptTestName, 1+round*4)
		if err != nil {
			t.Fatal(err)
		}
		if got := describeTree(t, again); got != want {
			t.Fatalf("round %d: tree differs", round)
		}
	}
	for _, row := range rows[:200] {
		path, _ := first.PathOf(row.ID)
		if entry, ok := first.Resolve(path); !ok || entry.ID != row.ID {
			t.Fatalf("Resolve(PathOf(%s)) = %v", row.ID, entry.ID)
		}
	}
}

func TestBuildFailsOnBadRows(t *testing.T) {
	cases := map[string]struct {
		rows []EncryptedEntry
		want error
	}{
		"duplicate name": {[]EncryptedEntry{
			{ID: "a", ParentID: model.RootID, EncName: encryptTestName("x"), Kind: model.KindFile},
			{ID: "b", ParentID: model.RootID, EncName: encryptTestName("x"), Kind: model.KindFile},
		}, fs.ErrExist},
		"missing parent": {[]EncryptedEntry{
			{ID: "a", ParentID: "ghost", EncName: encryptTestName("x"), Kind: model.KindFile},
		}, fs.ErrNotExist},
		"cycle": {[]EncryptedEntry{
			{ID: "a", ParentID: "b", EncName: encryptTestName("a"), Kind: model.KindFolder},
			{ID: "b", ParentID: "a", EncName: encryptTestName("b"), Kind: model.KindFolder},
		}, nil},
		"undecryptable": {[]EncryptedEntry{
			{ID: "a", ParentID: model.RootID, EncName: "garbage", Kind: model.KindFile},
		}, nil},
	}
	for name, c := range cases {
		_, err := Build(context.Background(), rowsOf(c.rows), decryptTestName, 4)
		if err == nil || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}

func TestBuildStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Build(ctx, rowsOf(generateRows(1000, 1)), decryptTestName, 4); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
