package drivefake

import (
	"encoding/json"
	"net/http"
	"strings"
)

const (
	defaultFileMask    = "kind,id,name,mimeType"
	defaultListMask    = "kind,nextPageToken,incompleteSearch,files(kind,id,name,mimeType)"
	defaultChangesMask = "kind,nextPageToken,newStartPageToken,changes(kind,changeType,time,removed,fileId,file(kind,id,name,mimeType))"
)

// apiError is a Drive-shaped failure; handlers return it and the server writes the
// googleapis error envelope that the client library parses.
type apiError struct {
	status  int
	reason  string
	message string
}

func (e *apiError) Error() string { return e.message }

func failure(status int, reason, message string) *apiError {
	return &apiError{status: status, reason: reason, message: message}
}

func badRequest(message string) *apiError {
	return failure(http.StatusBadRequest, "invalid", message)
}

func notFound(id string) *apiError {
	return failure(http.StatusNotFound, "notFound", "File not found: "+id)
}

func writeError(w http.ResponseWriter, err *apiError) {
	body := map[string]any{
		"error": map[string]any{
			"code":    err.status,
			"message": err.message,
			"errors":  []map[string]string{{"domain": "global", "reason": err.reason, "message": err.message}},
		},
	}
	writeJSON(w, err.status, body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

// writeMasked applies Drive's partial-response "fields" mask, so a client that forgets to
// ask for a field really does not receive it.
func writeMasked(w http.ResponseWriter, status int, value any, fields, defaultMask string) {
	if fields == "" {
		fields = defaultMask
	}
	mask, err := parseFieldMask(fields)
	if err != nil {
		writeError(w, badRequest(err.Error()))
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var generic any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, status, mask.apply(generic))
}

type fieldMask map[string]fieldMask

func (m fieldMask) apply(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		kept := make(map[string]any, len(m))
		for key, child := range m {
			if field, ok := typed[key]; ok {
				kept[key] = child.applyChild(field)
			}
		}
		return kept
	case []any:
		for i, item := range typed {
			typed[i] = m.apply(item)
		}
		return typed
	}
	return value
}

func (m fieldMask) applyChild(value any) any {
	if m == nil {
		return value
	}
	return m.apply(value)
}

func parseFieldMask(fields string) (fieldMask, error) {
	mask, rest, err := parseFieldList(strings.ReplaceAll(fields, " ", ""))
	if err != nil {
		return nil, err
	}
	if rest != "" {
		return nil, badRequest("invalid field selection " + fields)
	}
	return mask, nil
}

func parseFieldList(input string) (fieldMask, string, error) {
	mask := fieldMask{}
	for {
		end := strings.IndexAny(input, ",()")
		if end < 0 {
			end = len(input)
		}
		name := input[:end]
		if name == "" {
			return nil, "", badRequest("empty field name in selection")
		}
		input = input[end:]
		var child fieldMask
		if strings.HasPrefix(input, "(") {
			var err error
			child, input, err = parseFieldList(input[1:])
			if err != nil {
				return nil, "", err
			}
			if !strings.HasPrefix(input, ")") {
				return nil, "", badRequest("unbalanced parentheses in field selection")
			}
			input = input[1:]
		}
		mask[name] = child
		if !strings.HasPrefix(input, ",") {
			return mask, input, nil
		}
		input = input[1:]
	}
}
