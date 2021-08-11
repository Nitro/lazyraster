package service

import (
	"context"
	"io"
)

type bypassCache string

// BypassKey key is used to bypass the operation of the service.
var BypassKey bypassCache = "bypassCacheKey" // nolint: gochecknoglobals

type bypassService interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, payload io.Reader) error
}

// Bypass is used to bypass the operations of this struct.
type Bypass struct {
	Service bypassService
}

// Get object.
func (b Bypass) Get(ctx context.Context, key string) (_ io.ReadCloser, err error) {
	if b.bypass(ctx) {
		return nil, nil
	}
	return b.Service.Get(ctx, key)
}

// Put a object.
func (b Bypass) Put(ctx context.Context, key string, payload io.Reader) (err error) {
	if b.bypass(ctx) {
		return nil
	}
	return b.Service.Put(ctx, key, payload)
}

func (Bypass) bypass(ctx context.Context) bool {
	bypass, _ := ctx.Value(BypassKey).(bool)
	return bypass
}
