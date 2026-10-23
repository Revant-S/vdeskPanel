package syncer_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"sync/atomic"
	"testing"

	"vault/internal/devicetest"
	"vault/internal/drive/drivefake"
)

func TestKilledUploadResumesFromConfirmedOffset(t *testing.T) {
	n := devicetest.NewNetwork(t)
	a := n.NewDevice(t, "a")
	video := make([]byte, 8<<20)
	rand.Read(video)
	a.WriteFile("/video.mp4", video)

	var chunks atomic.Int32
	afterTwelveChunks := func(r *http.Request) bool {
		isChunk := r.Method == http.MethodPut && drivefake.KindOf(r) == drivefake.KindUpload && r.ContentLength > 0
		return isChunk && chunks.Add(1) > 12
	}
	n.Drive.InjectFault(drivefake.Fault{Match: afterTwelveChunks, Status: 503, Times: 1000})
	if err := a.Outgoing.FlushNow(context.Background()); err == nil {
		t.Fatal("upload finished although the network died")
	}
	n.Drive.ClearFaults()
	before := len(n.Drive.Requests())
	a.Restart()
	a.Sync()

	lowest := int64(-1)
	for _, request := range n.Drive.Requests()[before:] {
		if request.Kind == drivefake.KindUpload && request.Method == http.MethodPut && request.Bytes > 0 {
			if lowest < 0 || request.Offset < lowest {
				lowest = request.Offset
			}
		}
	}
	if lowest != 12*256<<10 {
		t.Fatalf("after restart the upload resent from byte %d; want a resume at %d", lowest, 12*256<<10)
	}
	if !bytes.Equal(a.ReadFile("/video.mp4"), video) {
		t.Fatal("content wrong after resumed upload")
	}
	object := objectOf(t, a, a.ObjectID("/video.mp4"))
	remote, _, _ := n.Drive.File(object.DriveID)
	if remote.MD5 != object.BodyMD5 || remote.Size < int64(len(video)) {
		t.Fatalf("remote %+v, row md5 %s", remote, object.BodyMD5)
	}
}
