package cache_test

import (
	"github.com/cosmoboy/vault/internal/cache"
	"github.com/cosmoboy/vault/internal/index"
)

var _ cache.BitmapStore = (*index.Store)(nil)
