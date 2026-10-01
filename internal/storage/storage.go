// Package storage is the object store Pail keeps every pail in. In
// production that is versitygw, spoken to over S3; tests use Memory.
package storage

import (
	"context"
	"errors"
	"io"
)

var ErrNotFound = errors.New("storage: no such object")

type Store interface {
	// Put writes size bytes from body to key, replacing what was there.
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	// Read returns a whole object, or ErrNotFound.
	Read(ctx context.Context, key string) ([]byte, error)
	// Open returns an object for streaming. A missing object may only
	// surface as an error on the first Read or Seek.
	Open(ctx context.Context, key string) (io.ReadSeekCloser, error)
	// Copy duplicates one object inside the store.
	Copy(ctx context.Context, src, dst string) error
	// List returns every key under prefix.
	List(ctx context.Context, prefix string) ([]string, error)
	// DeletePrefix removes every object under prefix.
	DeletePrefix(ctx context.Context, prefix string) error
}
