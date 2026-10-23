package cache_test

import (
	"vault/internal/cache"
	"vault/internal/index"
)

var _ cache.BitmapStore = (*index.Store)(nil)
