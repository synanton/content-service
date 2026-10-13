package port

import (
	"context"
	"io"
	"time"
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

// BlobInfo describes one stored object for reconciliation scans.
type BlobInfo struct {
	Key        string
	Size       int64
	ModifiedAt time.Time
}

// BlobLister enumerates stored objects under a prefix ("" lists all).
// The orphan sweeper depends on it; BlobStore implementations that
// cannot list must return an explicit error, never a silent empty set.
type BlobLister interface {
	List(ctx context.Context, prefix string) ([]BlobInfo, error)
}
