package drive

import (
	"io"
	"net/http"

	"google.golang.org/api/googleapi"
)

func checkResponse(res *http.Response) error {
	if err := googleapi.CheckResponse(res); err != nil {
		return normalizeError(err)
	}
	return nil
}

func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	_ = body.Close()
}
