package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
)

func Compute(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

func Verify(data []byte, expected string) bool {
	return Compute(data) == expected
}

func NewHasher() hash.Hash {
	return sha256.New()
}

func ComputeFromReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
