package syncer_test

import (
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"vault/internal/devicetest"
	"vault/internal/drive"
	"vault/internal/drive/drivefake"
)

// The real figures are a Drive limit of about 3 creates per second against a budget of 3
// writes per second. Both are scaled by the same factor so the test runs in seconds while
// the ratio, which is what decides whether Drive pushes back, stays the same.
const timeScale = 100

func TestThousandsOfSmallFilesStayInsideDriveQuota(t *testing.T) {
	if os.Getenv("VAULT_BULK_TESTS") != "1" {
		t.Skip("bulk sync of 5,000 files; run with make test-bulk")
	}
	files := 5000
	n := devicetest.NewNetwork(t, drivefake.WithQuota(12000*timeScale, 3*timeScale, 10))
	a := n.NewDevice(t, "a")
	a.Config.Budget = drive.BudgetConfig{
		MetadataWritesPerSecond: 3 * timeScale * 0.9, MetadataWriteBurst: 10,
		ReadsPerSecond: 10 * timeScale, ReadBurst: 30,
	}
	a.Restart()
	a.Mkdir("/Bulk")
	for i := 0; i < files; i++ {
		a.WriteFile(fmt.Sprintf("/Bulk/file-%05d.txt", i), []byte(fmt.Sprintf("content %d", i)))
	}
	before := len(n.Drive.Requests())
	start := time.Now()
	a.Sync()
	pushback := 0
	for _, request := range n.Drive.Requests()[before:] {
		if request.Status == http.StatusTooManyRequests || request.Status == http.StatusForbidden {
			pushback++
		}
	}
	if pushback > 3 {
		t.Fatalf("Drive pushed back %d times", pushback)
	}
	bulk := objectOf(t, a, a.ObjectID("/Bulk"))
	if got := len(driveFilesUnder(t, n, bulk.DriveID)); got != files {
		t.Fatalf("Drive holds %d files, want %d", got, files)
	}
	t.Logf("synced %d files in %v with %d pushbacks", files, time.Since(start), pushback)
}
