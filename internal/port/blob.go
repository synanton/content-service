package port

import (
	"context"
	"io"
)

// BlobStore persists opaque payload bytes under content-addressed keys.
// Keys are opaque to callers; the postgres adapter derives them from
// tenant and digest (tenant + "/" + digest hex).
type BlobStore interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

// BlobReader streams large payloads without buffering them in memory.
// BlobStore implementations may additionally implement it; callers must
// type-assert before use.
type BlobReader interface {
	GetStream(ctx context.Context, key string) (io.ReadCloser, int64, error)
}
