// Package parquet reads and writes the content-addressed Parquet files
// backing the parquet storage tier. Files are immutable once written:
// the compactor merges small files into bigger ones and repoints artifact
// rows, never mutating a file in place.
package parquet

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress"
)

// Row is one payload record inside a Parquet file. Field order is the
// column order; tags select snappy (fast, good enough for already-dense
// serialized payloads — configurable per file at write time).
type Row struct {
	TenantID   string `parquet:"tenant_id,snappy"`
	ArtifactID string `parquet:"artifact_id,snappy"`
	Digest     string `parquet:"digest,snappy"`
	Content    []byte `parquet:"content,snappy"`
	CreatedAt  int64  `parquet:"created_at,snappy"`
}

// RangeGetter fetches byte ranges of one object. Implemented by the S3
// adapter; the reader only needs ranged reads, never full downloads.
type RangeGetter interface {
	Size(ctx context.Context, key string) (int64, error)
	Range(ctx context.Context, key string, offset, length int64) ([]byte, error)
}

// EncodeAll writes rows as one Parquet file using the named compression
// ("snappy", "zstd", "gzip"; anything else fails fast).
func EncodeAll(rows []Row, compression string) ([]byte, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("parquet: nothing to encode")
	}
	var codec compress.Codec
	switch compression {
	case "", "snappy":
		codec = &parquet.Snappy
	case "zstd":
		codec = &parquet.Zstd
	case "gzip":
		codec = &parquet.Gzip
	default:
		return nil, fmt.Errorf("parquet: unknown compression %q", compression)
	}
	var buf bytes.Buffer
	writer := parquet.NewGenericWriter[Row](&buf, parquet.Compression(codec))
	if _, err := writer.Write(rows); err != nil {
		return nil, fmt.Errorf("parquet: write rows: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("parquet: close writer: %w", err)
	}
	return buf.Bytes(), nil
}

// DecodeAll reads every row of an in-memory file (compactor path).
func DecodeAll(data []byte) ([]Row, error) {
	reader := parquet.NewGenericReader[Row](bytes.NewReader(data))
	rows := make([]Row, 0, 64)
	buf := make([]Row, 64)
	for {
		n, err := reader.Read(buf)
		rows = append(rows, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parquet: read rows: %w", err)
		}
	}
	return rows, nil
}

// Reader fetches single rows from files in object storage with ranged
// reads: opening the footer plus seeking to one row, never full downloads.
type Reader struct {
	blobs RangeGetter
}

// NewReader builds a row reader over a range-capable backend.
func NewReader(blobs RangeGetter) *Reader {
	return &Reader{blobs: blobs}
}

// ReadRow returns row idx of the file at key.
func (r *Reader) ReadRow(ctx context.Context, key string, idx int64) (Row, error) {
	var zero Row
	size, err := r.blobs.Size(ctx, key)
	if err != nil {
		return zero, err
	}
	input := &rangeInput{ctx: ctx, blobs: r.blobs, key: key, size: size}
	reader := parquet.NewGenericReader[Row](input)
	if err := reader.SeekToRow(idx); err != nil {
		return zero, fmt.Errorf("parquet: seek row %d of %s: %w", idx, key, err)
	}
	rows := make([]Row, 1)
	n, err := reader.Read(rows)
	if err != nil && err != io.EOF {
		return zero, fmt.Errorf("parquet: read row %d of %s: %w", idx, key, err)
	}
	if n != 1 {
		return zero, fmt.Errorf("parquet: row %d of %s missing", idx, key)
	}
	return rows[0], nil
}

type rangeInput struct {
	ctx   context.Context
	blobs RangeGetter
	key   string
	size  int64
	off   int64
}

func (r *rangeInput) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.size {
		return 0, io.EOF
	}
	data, err := r.blobs.Range(r.ctx, r.key, off, int64(len(p)))
	if err != nil {
		return 0, err
	}
	return copy(p, data), nil
}

func (r *rangeInput) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = r.off
	case io.SeekEnd:
		base = r.size
	default:
		return 0, fmt.Errorf("parquet: invalid whence %d", whence)
	}
	r.off = base + offset
	return r.off, nil
}

func (r *rangeInput) Read(p []byte) (int, error) {
	n, err := r.ReadAt(p, r.off)
	r.off += int64(n)
	return n, err
}
