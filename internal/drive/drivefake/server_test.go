package drivefake

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func send(t *testing.T, server *Server, method, path, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, InProcessURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := server.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	answer, _ := io.ReadAll(res.Body)
	return res, string(answer)
}

func createFolder(t *testing.T, server *Server, metadata string) string {
	t.Helper()
	res, body := send(t, server, http.MethodPost, "/drive/v3/files", metadata)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create: %d %s", res.StatusCode, body)
	}
	var created struct{ ID string }
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	return created.ID
}

func listQuery(t *testing.T, server *Server, query string) (int, string) {
	t.Helper()
	res, body := send(t, server, http.MethodGet, "/drive/v3/files?"+url.Values{"q": {query}, "fields": {"files(id,name)"}}.Encode(), "")
	return res.StatusCode, body
}

func TestQueryParserRejectsAnythingOutsideTheSubset(t *testing.T) {
	server := New()
	for _, query := range []string{
		"name contains 'x'",
		"fullText contains 'secret'",
		"'a' in parents or name = 'b'",
		"modifiedTime > '2026-01-01T00:00:00'",
		"trashed = maybe",
		"name = 'unterminated",
		"not trashed = true",
		"appProperties has { key='k' }",
		"name = 'a' and",
		"(name = 'a')",
		"name != 'a'",
	} {
		if status, body := listQuery(t, server, query); status != http.StatusBadRequest {
			t.Errorf("query %q: status %d (%s), want 400", query, status, body)
		}
	}
}

func TestQueryParserMatchesTheSubset(t *testing.T) {
	server := New()
	parent := createFolder(t, server, `{"name":"tree","mimeType":"application/vnd.google-apps.folder"}`)
	createFolder(t, server, `{"name":"it's","parents":["`+parent+`"],"appProperties":{"oid":"o1"},"mimeType":"application/vnd.google-apps.folder"}`)
	createFolder(t, server, `{"name":"other","parents":["`+parent+`"],"appProperties":{"oid":"o2"}}`)
	cases := map[string]int{
		"'" + parent + "' in parents and trashed = false":                                 2,
		"'" + parent + "' in parents and name = 'it\\'s'":                                 1,
		"appProperties has { key='oid' and value='o2' } and trashed = false":              1,
		"mimeType = 'application/vnd.google-apps.folder' and '" + parent + "' in parents": 1,
		"trashed = true": 0,
		"appProperties has { key='oid' and value='o1' } and appProperties has { key='oid' and value='o2' }": 0,
	}
	for query, want := range cases {
		status, body := listQuery(t, server, query)
		var list struct{ Files []struct{ ID string } }
		if err := json.Unmarshal([]byte(body), &list); err != nil || status != http.StatusOK || len(list.Files) != want {
			t.Errorf("query %q: status %d, %d files (%s), want %d", query, status, len(list.Files), body, want)
		}
	}
}

func TestFieldsMaskLimitsTheResponse(t *testing.T) {
	server := New()
	id := createFolder(t, server, `{"name":"tree","appProperties":{"vid":"v"}}`)
	_, body := send(t, server, http.MethodGet, "/drive/v3/files/"+id, "")
	if strings.Contains(body, "appProperties") || !strings.Contains(body, `"name":"tree"`) {
		t.Fatalf("default mask returned %s", body)
	}
	_, body = send(t, server, http.MethodGet, "/drive/v3/files/"+id+"?fields=id,appProperties", "")
	if strings.Contains(body, "name") || !strings.Contains(body, `"vid":"v"`) {
		t.Fatalf("explicit mask returned %s", body)
	}
	if res, _ := send(t, server, http.MethodGet, "/drive/v3/files/"+id+"?fields=files(id", ""); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed mask: status %d, want 400", res.StatusCode)
	}
}

func TestQuotaAnswersLikeDrive(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	server := New(WithNow(func() time.Time { return now }), WithQuota(5, 1, 2))
	statuses := make([]int, 0, 4)
	for range 3 {
		res, _ := send(t, server, http.MethodPost, "/drive/v3/files", `{"name":"x"}`)
		statuses = append(statuses, res.StatusCode)
	}
	if statuses[0] != http.StatusOK || statuses[1] != http.StatusOK || statuses[2] != http.StatusTooManyRequests {
		t.Fatalf("create statuses %v, want 200 200 429", statuses)
	}
	send(t, server, http.MethodGet, "/drive/v3/changes/startPageToken", "")
	send(t, server, http.MethodGet, "/drive/v3/changes/startPageToken", "")
	res, body := send(t, server, http.MethodGet, "/drive/v3/changes/startPageToken", "")
	if res.StatusCode != http.StatusForbidden || !strings.Contains(body, "userRateLimitExceeded") {
		t.Fatalf("sixth request: %d %s, want 403 userRateLimitExceeded", res.StatusCode, body)
	}
}

func TestResumableSessionWithTakenIDConflicts(t *testing.T) {
	server := New()
	_, body := send(t, server, http.MethodGet, "/drive/v3/files/generateIds?count=1", "")
	var generated struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal([]byte(body), &generated); err != nil || len(generated.IDs) != 1 {
		t.Fatalf("generateIds: %s", body)
	}
	createFolder(t, server, `{"id":"`+generated.IDs[0]+`","name":"x"}`)
	req, _ := http.NewRequest(http.MethodPost, InProcessURL+"/upload/drive/v3/files?uploadType=resumable", strings.NewReader(`{"id":"`+generated.IDs[0]+`"}`))
	req.Header.Set("X-Upload-Content-Length", "10")
	res, err := server.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status %d, want 409", res.StatusCode)
	}
}

func TestCorruptContentKeepsTheChecksum(t *testing.T) {
	server := New()
	req, _ := http.NewRequest(http.MethodPost, InProcessURL+"/upload/drive/v3/files?uploadType=resumable", strings.NewReader(`{"name":"body"}`))
	req.Header.Set("X-Upload-Content-Length", "3")
	res, err := server.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	chunk, _ := http.NewRequest(http.MethodPut, res.Header.Get("Location"), strings.NewReader("abc"))
	chunk.Header.Set("Content-Range", "bytes 0-2/3")
	res, err = server.HTTPClient().Do(chunk)
	if err != nil {
		t.Fatal(err)
	}
	var created struct{ ID string }
	json.NewDecoder(res.Body).Decode(&created)
	res.Body.Close()
	before, _, _ := server.File(created.ID)
	server.CorruptContent(created.ID, 1)
	after, content, _ := server.File(created.ID)
	if string(content) == "abc" || after.MD5 != before.MD5 {
		t.Fatalf("corrupted content %q, md5 %s→%s", content, before.MD5, after.MD5)
	}
}
