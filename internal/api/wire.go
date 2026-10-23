package api

import (
	"fmt"
	"net/url"
	"strconv"
)

type idBody struct {
	ID string `json:"id"`
}

type pathBody struct {
	Path string `json:"path"`
}

type authURLBody struct {
	AuthURL string `json:"authURL"`
}

func parseBoolQuery(query url.Values, name string) (bool, error) {
	text := query.Get(name)
	if text == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(text)
	if err != nil {
		return false, fmt.Errorf("%w: %s must be true or false", ErrInvalidRequest, name)
	}
	return value, nil
}

func parseLimitQuery(query url.Values) (int, error) {
	text := query.Get("limit")
	if text == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(text)
	if err != nil || limit < 0 {
		return 0, fmt.Errorf("%w: limit must be a non-negative integer", ErrInvalidRequest)
	}
	return limit, nil
}

func parseJoinMode(text string) (JoinMode, error) {
	switch JoinMode(text) {
	case "", JoinRestoreEverything:
		return JoinRestoreEverything, nil
	case JoinOnDemand:
		return JoinOnDemand, nil
	}
	return "", fmt.Errorf("%w: mode must be %s or %s", ErrInvalidRequest, JoinRestoreEverything, JoinOnDemand)
}
