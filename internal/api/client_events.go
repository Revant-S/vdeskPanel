package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

func (c *Client) streamEvents(ctx context.Context, events chan<- Event) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://vault/events", nil)
	if err != nil {
		return
	}
	response, err := c.http.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var event Event
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		select {
		case events <- event:
		case <-ctx.Done():
			return
		}
	}
}
